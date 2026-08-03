import { describe, expect, it } from 'vitest';

import { WebhookUrlGuard } from './webhook-url-guard.js';
import { WebhookUrlRejected } from '../../application/ports.js';

function guard(dns: Record<string, string[]> = {}, requireHttps = true, allowPrivate = false) {
  return new WebhookUrlGuard({
    requireHttps,
    allowPrivateNetworks: allowPrivate,
    resolve: async (hostname) => {
      const answer = dns[hostname];
      if (!answer) throw new Error('ENOTFOUND');
      return answer;
    },
  });
}

const DNS = {
  'client.example.com': ['93.184.216.34'],
  // A public-looking hostname pointing straight at the metadata service.
  'evil.example.com': ['169.254.169.254'],
  'inside.example.com': ['10.0.0.5'],
  // Round-robin where only one answer is dangerous: the dialer's choice is
  // not ours to predict, so the whole host has to be refused.
  'mixed.example.com': ['93.184.216.34', '127.0.0.1'],
};

describe('WebhookUrlGuard — OWASP A10', () => {
  it('accepts a public https endpoint', async () => {
    await expect(
      guard(DNS).assertAllowed('https://client.example.com/hooks/payments'),
    ).resolves.toBeUndefined();
  });

  it.each([
    ['plain http', 'http://client.example.com/hooks', 'SCHEME_NOT_ALLOWED'],
    ['file scheme', 'file:///etc/passwd', 'INVALID_WEBHOOK_SCHEME'],
    ['gopher scheme', 'gopher://client.example.com/', 'INVALID_WEBHOOK_SCHEME'],
  ])('rejects %s', async (_name, url, code) => {
    await expect(guard(DNS).assertAllowed(url)).rejects.toMatchObject({ code });
  });

  it.each([
    ['loopback literal', 'https://127.0.0.1/hooks'],
    ['ipv6 loopback literal', 'https://[::1]/hooks'],
    ['ipv4-mapped loopback', 'https://[::ffff:127.0.0.1]/hooks'],
    ['private 10/8', 'https://10.0.0.5/hooks'],
    ['private 192.168/16', 'https://192.168.1.10/hooks'],
    ['private 172.16/12', 'https://172.20.1.1/hooks'],
    ['cloud metadata', 'https://169.254.169.254/latest/meta-data/'],
    ['carrier-grade NAT', 'https://100.100.0.1/hooks'],
    ['unspecified', 'https://0.0.0.0/hooks'],
    ['unique local ipv6', 'https://[fd00::1]/hooks'],
    ['link-local ipv6', 'https://[fe80::1]/hooks'],
  ])('rejects %s', async (_name, url) => {
    await expect(guard(DNS).assertAllowed(url)).rejects.toMatchObject({
      code: 'ADDRESS_BLOCKED',
    });
  });

  it.each([
    ['a hostname resolving to cloud metadata', 'https://evil.example.com/hooks'],
    ['a hostname resolving into the private network', 'https://inside.example.com/hooks'],
    ['a hostname with one dangerous answer among several', 'https://mixed.example.com/hooks'],
  ])('rejects %s', async (_name, url) => {
    await expect(guard(DNS).assertAllowed(url)).rejects.toMatchObject({
      code: 'ADDRESS_BLOCKED',
    });
  });

  it('rejects a host that cannot be resolved', async () => {
    await expect(
      guard(DNS).assertAllowed('https://nowhere.example.com/hooks'),
    ).rejects.toMatchObject({ code: 'HOST_NOT_RESOLVABLE' });
  });

  it.each([
    ['ssh', 22],
    ['postgres', 5432],
    ['redis', 6379],
    ['kafka', 9092],
    ['a privileged port', 161],
  ])('rejects port scanning disguised as a webhook: %s', async (_name, port) => {
    await expect(
      guard(DNS).assertAllowed(`https://client.example.com:${port}/hooks`),
    ).rejects.toMatchObject({ code: 'PORT_NOT_ALLOWED' });
  });

  it.each([443, 8443, 52341])(
    'still allows legitimate endpoints on port %s',
    async (port) => {
      await expect(
        guard(DNS).assertAllowed(`https://client.example.com:${port}/hooks`),
      ).resolves.toBeUndefined();
    },
  );

  it('rejects credentials embedded in the URL', async () => {
    // Anything here would be written to the database and echoed in logs.
    await expect(
      guard(DNS).assertAllowed('https://user:secret@client.example.com/hooks'),
    ).rejects.toMatchObject({ code: 'CREDENTIALS_IN_WEBHOOK_URL' });
  });

  it('relaxes only what the operator explicitly turned off', async () => {
    const local = { 'demo-tools': ['172.20.0.5'] };

    await expect(
      guard(local).assertAllowed('http://demo-tools:3004/webhook'),
    ).rejects.toBeInstanceOf(WebhookUrlRejected);

    await expect(
      guard(local, false, true).assertAllowed('http://demo-tools:3004/webhook'),
    ).resolves.toBeUndefined();
  });
});
