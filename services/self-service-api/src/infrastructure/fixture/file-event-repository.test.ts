import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import type { NotificationEventRepository } from '../../application/ports.js';

import { FileNotificationEventRepository } from './file-event-repository.js';
import { notificationEventIdFor } from './fixture-mapping.js';

/**
 * These run against the real `fixtures/notification_events.json` — the file
 * attached to the case statement, unmodified. If someone edits it, these fail.
 */
const FIXTURE = resolve(import.meta.dirname, '../../../../../fixtures/notification_events.json');

function repository() {
  return new FileNotificationEventRepository(FIXTURE, 'https://client.example.com/webhooks');
}

const page = { page: 1, pageSize: 50 };

describe('FileNotificationEventRepository — the case statement’s own fixture', () => {
  it('loads the ten events and scopes them by client', async () => {
    const repo = repository();

    const clientOne = await repo.findPage({ clientId: 'CLIENT001', ...page });
    const clientTwo = await repo.findPage({ clientId: 'CLIENT002', ...page });
    const clientThree = await repo.findPage({ clientId: 'CLIENT003', ...page });

    expect(clientOne.total + clientTwo.total + clientThree.total).toBe(10);
    for (const event of clientOne.items) {
      expect(event.clientId).toBe('CLIENT001');
    }
  });

  it('preserves the upstream identifiers verbatim', async () => {
    const result = await repository().findPage({ clientId: 'CLIENT001', ...page });
    const ids = result.items.map((e) => e.eventId);

    // EVT001 / CLIENT001 do not fit a UUID column, which is exactly why
    // upstream identifiers are stored as opaque strings.
    expect(ids).toContain('EVT001');
  });

  /**
   * The three `failed` events in the fixture are what the replay requirement
   * acts on. Their presence is not incidental — it is the sample data the
   * brief provides to exercise "re-send a notification when delivery has
   * definitely failed".
   */
  it('maps delivery_status onto the platform’s lifecycle', async () => {
    const repo = repository();

    const failed = await Promise.all(
      ['CLIENT001', 'CLIENT002', 'CLIENT003'].map((clientId) =>
        repo.findPage({ clientId, state: 'FAILED', ...page }),
      ),
    );
    const delivered = await Promise.all(
      ['CLIENT001', 'CLIENT002', 'CLIENT003'].map((clientId) =>
        repo.findPage({ clientId, state: 'DELIVERED', ...page }),
      ),
    );

    expect(failed.reduce((sum, p) => sum + p.total, 0)).toBe(3);
    expect(delivered.reduce((sum, p) => sum + p.total, 0)).toBe(7);

    const failedIds = failed.flatMap((p) => p.items.map((e) => e.eventId)).sort();
    expect(failedIds).toEqual(['EVT003', 'EVT005', 'EVT009']);
  });

  it('filters by creation date, taken from delivery_date', async () => {
    const result = await repository().findPage({
      clientId: 'CLIENT001',
      createdFrom: new Date('2024-03-15T15:00:00Z'),
      ...page,
    });

    expect(result.items.map((e) => e.eventId).sort()).toEqual(['EVT007', 'EVT010']);
  });

  it('derives a stable notification_event_id, so links keep working', async () => {
    const first = await repository().findPage({ clientId: 'CLIENT001', ...page });
    const second = await repository().findPage({ clientId: 'CLIENT001', ...page });

    const idOf = (p: typeof first, eventId: string) =>
      p.items.find((e) => e.eventId === eventId)?.id;

    expect(idOf(first, 'EVT001')).toBe(idOf(second, 'EVT001'));
    expect(idOf(first, 'EVT001')).toBe(notificationEventIdFor('EVT001'));
  });

  it('returns a delivery attempt consistent with the recorded outcome', async () => {
    const repo = repository();
    const failedId = notificationEventIdFor('EVT003');

    const detail = await repo.findByIdForClient(failedId, 'CLIENT002');
    expect(detail?.attempts[0]).toMatchObject({ status: 'FAILED', responseStatus: 500 });

    const deliveredDetail = await repo.findByIdForClient(
      notificationEventIdFor('EVT001'),
      'CLIENT001',
    );
    expect(deliveredDetail?.attempts[0]).toMatchObject({ status: 'SUCCESS', responseStatus: 200 });
  });

  it('enforces ownership exactly like the database adapter', async () => {
    const repo = repository();
    // EVT003 belongs to CLIENT002.
    const stolen = await repo.findByIdForClient(notificationEventIdFor('EVT003'), 'CLIENT001');
    expect(stolen).toBeNull();
  });

  it('paginates', async () => {
    const repo = repository();
    const first = await repo.findPage({ clientId: 'CLIENT001', page: 1, pageSize: 2 });
    const second = await repo.findPage({ clientId: 'CLIENT001', page: 2, pageSize: 2 });

    expect(first.items).toHaveLength(2);
    expect(first.total).toBe(4);
    expect(second.items[0]?.id).not.toBe(first.items[0]?.id);
  });

  it('has no markForReplay, so the read-only limit is a type-level fact', () => {
    // Seen through the port, the optional method is simply absent — the
    // compiler already refuses `repository().markForReplay` outright.
    const asPort: NotificationEventRepository = repository();
    expect(asPort.markForReplay).toBeUndefined();
  });
});
