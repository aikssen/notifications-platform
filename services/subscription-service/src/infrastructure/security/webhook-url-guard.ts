import { lookup } from 'node:dns/promises';
import { isIP } from 'node:net';

import { assertSyntacticallyValidWebhookUrl } from '../../domain/subscription.js';
import { WebhookUrlRejected, type WebhookUrlValidator } from '../../application/ports.js';

/**
 * OWASP A10 — Server-Side Request Forgery.
 *
 * This endpoint lets a client tell the platform which URL to call. Without a
 * guard, that is a request forgery primitive handed straight to the internet:
 * point a subscription at 169.254.169.254 and the platform fetches cloud
 * credentials on the client's behalf; point it at an internal host and the
 * platform maps the private network for them, one subscription at a time.
 *
 * Rejecting at registration is the first of two layers. The dispatcher runs
 * the same checks again before every call, and once more at socket level —
 * because a hostname that resolves to a public address today can resolve to
 * 127.0.0.1 tomorrow, and validating only at registration would never notice.
 */

const DENIED_PORTS = new Map<number, string>([
  [22, 'ssh'], [23, 'telnet'], [25, 'smtp'], [111, 'rpcbind'],
  [135, 'msrpc'], [139, 'netbios'], [445, 'smb'], [1433, 'mssql'],
  [1521, 'oracle'], [2049, 'nfs'], [2375, 'docker'], [2379, 'etcd'],
  [3306, 'mysql'], [3389, 'rdp'], [5432, 'postgres'], [5672, 'amqp'],
  [6379, 'redis'], [9042, 'cassandra'], [9092, 'kafka'],
  [9200, 'elasticsearch'], [11211, 'memcached'], [27017, 'mongodb'],
]);

export interface WebhookUrlGuardOptions {
  /**
   * True in any real environment. The local demo runs the client webhook over
   * plain HTTP inside the Docker network, and turning this off is a visible,
   * logged decision rather than a quietly weakened validator.
   */
  requireHttps: boolean;
  allowPrivateNetworks: boolean;
  /** Injectable so the rules are testable without real DNS. */
  resolve?: (hostname: string) => Promise<string[]>;
}

export class WebhookUrlGuard implements WebhookUrlValidator {
  private readonly resolve: (hostname: string) => Promise<string[]>;

  constructor(private readonly options: WebhookUrlGuardOptions) {
    this.resolve = options.resolve ?? defaultResolve;
  }

  async assertAllowed(raw: string): Promise<void> {
    const url = assertSyntacticallyValidWebhookUrl(raw);

    if (this.options.requireHttps && url.protocol !== 'https:') {
      throw new WebhookUrlRejected(
        'webhook_url must use https',
        'SCHEME_NOT_ALLOWED',
      );
    }

    this.assertPortAllowed(url);

    const literal = stripBrackets(url.hostname);
    if (isIP(literal)) {
      this.assertAddressAllowed(literal);
      return;
    }

    let addresses: string[];
    try {
      addresses = await this.resolve(url.hostname);
    } catch {
      throw new WebhookUrlRejected(
        `webhook_url host ${url.hostname} cannot be resolved`,
        'HOST_NOT_RESOLVABLE',
      );
    }

    if (addresses.length === 0) {
      throw new WebhookUrlRejected(
        `webhook_url host ${url.hostname} resolved to nothing`,
        'HOST_NOT_RESOLVABLE',
      );
    }

    // Every answer must be acceptable. Which one the dialer would pick is not
    // ours to predict, so a single blocked address rejects the host.
    for (const address of addresses) {
      this.assertAddressAllowed(address);
    }
  }

  private assertPortAllowed(url: URL): void {
    if (!url.port) return;

    const port = Number(url.port);
    const service = DENIED_PORTS.get(port);
    if (service) {
      throw new WebhookUrlRejected(
        `port ${port} is ${service}, not a webhook endpoint`,
        'PORT_NOT_ALLOWED',
      );
    }
    if (port < 1024 && port !== 80 && port !== 443) {
      throw new WebhookUrlRejected(
        `port ${port} is a privileged port`,
        'PORT_NOT_ALLOWED',
      );
    }
  }

  private assertAddressAllowed(address: string): void {
    if (this.options.allowPrivateNetworks) return;

    const reason = blockedReason(address);
    if (reason) {
      throw new WebhookUrlRejected(
        `webhook_url resolves to ${address}, which is ${reason}`,
        'ADDRESS_BLOCKED',
      );
    }
  }
}

async function defaultResolve(hostname: string): Promise<string[]> {
  const results = await lookup(hostname, { all: true });
  return results.map((r) => r.address);
}

function stripBrackets(hostname: string): string {
  return hostname.startsWith('[') && hostname.endsWith(']')
    ? hostname.slice(1, -1)
    : hostname;
}

/** Returns why an address is blocked, or null when it is acceptable. */
export function blockedReason(address: string): string | null {
  const version = isIP(address);
  if (version === 4) return blockedReasonV4(address);
  if (version === 6) return blockedReasonV6(address);
  return 'not a valid IP address';
}

function blockedReasonV4(address: string): string | null {
  const parts = address.split('.').map(Number);
  const [a = 0, b = 0] = parts;

  if (a === 0) return 'unspecified';
  if (a === 127) return 'loopback';
  if (a === 10) return 'a private address';
  if (a === 172 && b >= 16 && b <= 31) return 'a private address';
  if (a === 192 && b === 168) return 'a private address';
  // 169.254.0.0/16 — where cloud instance metadata services live.
  if (a === 169 && b === 254) return 'link-local (cloud metadata)';
  // RFC 6598 carrier-grade NAT: never a legitimate webhook destination.
  if (a === 100 && b >= 64 && b <= 127) return 'carrier-grade NAT space';
  if (a >= 224 && a <= 239) return 'multicast';
  if (a >= 240) return 'reserved';

  return null;
}

function blockedReasonV6(address: string): string | null {
  const groups = expandIpv6(address);
  if (!groups) return 'not a parseable IPv6 address';

  // IPv4-mapped and IPv4-compatible addresses are the classic way past a check
  // that only pattern-matches strings. `::ffff:127.0.0.1` does not survive URL
  // parsing as written — WHATWG normalises it to `::ffff:7f00:1` — so the
  // embedded address has to be recovered from the bits, not from the text.
  const embedded = embeddedIpv4(groups);
  if (embedded) return blockedReasonV4(embedded);

  if (groups.every((g) => g === 0)) return 'unspecified';
  if (groups.slice(0, 7).every((g) => g === 0) && groups[7] === 1) return 'loopback';

  const first = groups[0] ?? 0;
  if ((first & 0xfe00) === 0xfc00) return 'a unique local address';
  if ((first & 0xffc0) === 0xfe80) return 'link-local';
  if ((first & 0xff00) === 0xff00) return 'multicast';

  return null;
}

/** Returns the eight 16-bit groups of an IPv6 address, or null if unparseable. */
function expandIpv6(address: string): number[] | null {
  let text = address.toLowerCase().split('%')[0] ?? '';

  // A trailing dotted-quad (`::ffff:127.0.0.1`) becomes two hex groups.
  const dotted = /(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$/.exec(text);
  if (dotted?.[1]) {
    const octets = dotted[1].split('.').map(Number);
    if (octets.some((o) => !Number.isInteger(o) || o < 0 || o > 255)) return null;
    const [a = 0, b = 0, c = 0, d = 0] = octets;
    text =
      text.slice(0, dotted.index) +
      ((a << 8) | b).toString(16) +
      ':' +
      ((c << 8) | d).toString(16);
  }

  const [head, tail, ...rest] = text.split('::');
  if (rest.length > 0) return null;

  const toGroups = (part: string | undefined): number[] =>
    part ? part.split(':').filter(Boolean).map((g) => parseInt(g, 16)) : [];

  const left = toGroups(head);
  const right = toGroups(tail);

  if ([...left, ...right].some((g) => Number.isNaN(g) || g < 0 || g > 0xffff)) {
    return null;
  }

  if (tail === undefined) {
    return left.length === 8 ? left : null;
  }

  const fill = 8 - left.length - right.length;
  if (fill < 0) return null;

  return [...left, ...new Array<number>(fill).fill(0), ...right];
}

/** Recovers the IPv4 address embedded in a mapped or compatible IPv6 address. */
function embeddedIpv4(groups: number[]): string | null {
  const prefixIsZero = groups.slice(0, 5).every((g) => g === 0);
  if (!prefixIsZero) return null;

  const marker = groups[5] ?? 0;
  const isMapped = marker === 0xffff; // ::ffff:a.b.c.d
  const isCompatible = marker === 0; // ::a.b.c.d, deprecated but still routable

  if (!isMapped && !isCompatible) return null;

  const high = groups[6] ?? 0;
  const low = groups[7] ?? 0;

  // `::` and `::1` are handled by their own rules, not as embedded IPv4.
  if (isCompatible && high === 0 && low <= 1) return null;

  return [high >> 8, high & 0xff, low >> 8, low & 0xff].join('.');
}
