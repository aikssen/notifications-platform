import type { NextFunction, Request, Response } from 'express';
import jwt from 'jsonwebtoken';

/**
 * OWASP A01 (broken access control) and A07 (authentication failures).
 *
 * Each service verifies tokens itself rather than sharing a library. A
 * security boundary that several services import from one package moves at the
 * speed of the slowest deploy; duplicating eighty lines is the cheaper trade
 * (ADR 0003).
 *
 * The single most important line in this file is where `clientId` comes from:
 * the verified token, and nowhere else. The previous implementation read
 * `client_id` from the query string and the request body, which meant any
 * caller could read — and replay — any other client's notifications by editing
 * one parameter. Deriving identity from the request payload is the defect;
 * everything else here is supporting detail.
 */

export interface AuthenticatedRequest extends Request {
  clientId?: string;
}

export interface JwtOptions {
  secret: string;
  issuer: string;
  audience: string;
}

export function authenticate(options: JwtOptions) {
  return (req: AuthenticatedRequest, res: Response, next: NextFunction): void => {
    const header = req.header('authorization');
    if (!header?.startsWith('Bearer ')) {
      res.status(401).json({ error: 'unauthorized', message: 'A bearer token is required' });
      return;
    }

    try {
      const payload = jwt.verify(header.slice('Bearer '.length), options.secret, {
        // Pinning the algorithm is what stops the `alg: none` and
        // RS256-downgraded-to-HS256 forgeries: without it, the library trusts
        // an attacker-controlled header to choose how to verify.
        algorithms: ['HS256'],
        issuer: options.issuer,
        audience: options.audience,
      });

      const clientId =
        typeof payload === 'object' && payload !== null
          ? (payload as jwt.JwtPayload).sub
          : undefined;

      if (!clientId) {
        res.status(401).json({ error: 'unauthorized', message: 'The token has no subject' });
        return;
      }

      req.clientId = clientId;
      next();
    } catch {
      // Deliberately not echoing the library's reason: "signature invalid"
      // versus "expired" is free information for someone probing the endpoint.
      res.status(401).json({ error: 'unauthorized', message: 'Invalid or expired token' });
    }
  };
}

/** Reads the client identity a request was authenticated as. */
export function clientIdOf(req: AuthenticatedRequest): string {
  if (!req.clientId) {
    throw new Error('clientIdOf called on a request that was never authenticated');
  }
  return req.clientId;
}

/**
 * Issues a token. Demo only, and mounted behind an explicit flag: a real
 * deployment gets its tokens from the platform's identity provider.
 */
export function issueDemoToken(
  clientId: string,
  options: JwtOptions,
  ttlSeconds: number,
): { token: string; expiresIn: number } {
  const token = jwt.sign({}, options.secret, {
    algorithm: 'HS256',
    subject: clientId,
    issuer: options.issuer,
    audience: options.audience,
    expiresIn: ttlSeconds,
  });
  return { token, expiresIn: ttlSeconds };
}
