import { readFile } from 'node:fs/promises';

import {
  NotificationEvent,
  type NotificationEventFilters,
  type Page,
} from '../../domain/notification-event.js';
import type { NotificationEventDetail, NotificationEventRepository } from '../../application/ports.js';
import {
  notificationEventIdFor,
  synthesiseAttempt,
  toEventState,
  type FixtureFile,
} from './fixture-mapping.js';

/**
 * The second adapter behind NotificationEventRepository: the JSON file attached
 * to the case statement, served through the same port as PostgreSQL.
 *
 *   "implement basic functionality to obtain the list of notifications from a
 *    repository (the attached file named: notification_events.json)"
 *
 * The word "repository" in that sentence, in a brief that asks for hexagonal
 * architecture, is the point. The file is not a data format to import — it is
 * one implementation of the repository port. Set EVENTS_REPOSITORY=file and
 * every endpoint answers identically, with no other change anywhere.
 *
 * It is read-only: replay needs somewhere to record a state change, so the use
 * case reports REPLAY_NOT_SUPPORTED rather than pretending it worked. The
 * `markForReplay` method is simply absent, so that is a type-level fact rather
 * than a runtime surprise.
 */
export class FileNotificationEventRepository implements NotificationEventRepository {
  private cache: NotificationEvent[] | null = null;

  constructor(
    private readonly fixturePath: string,
    private readonly webhookUrl: string,
  ) {}

  async findPage(filters: NotificationEventFilters): Promise<Page<NotificationEvent>> {
    const all = await this.load();

    const matching = all
      .filter((event) => event.belongsTo(filters.clientId))
      .filter((event) => !filters.state || event.state === filters.state)
      .filter((event) => !filters.createdFrom || event.createdAt >= filters.createdFrom)
      .filter((event) => !filters.createdTo || event.createdAt <= filters.createdTo)
      .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime());

    const offset = (filters.page - 1) * filters.pageSize;

    return {
      items: matching.slice(offset, offset + filters.pageSize),
      page: filters.page,
      pageSize: filters.pageSize,
      total: matching.length,
    };
  }

  async findByIdForClient(id: string, clientId: string): Promise<NotificationEventDetail | null> {
    const all = await this.load();
    const event = all.find((e) => e.id === id && e.belongsTo(clientId));
    if (!event) return null;

    const raw = (await this.readFixture()).events.find(
      (e) => notificationEventIdFor(e.event_id) === id,
    );

    return {
      event,
      attempts: raw ? [synthesiseAttempt(raw, this.webhookUrl)] : [],
    };
  }

  private async load(): Promise<NotificationEvent[]> {
    if (this.cache) return this.cache;

    const fixture = await this.readFixture();

    this.cache = fixture.events.map((raw) => {
      const timestamp = new Date(raw.delivery_date);
      return new NotificationEvent({
        id: notificationEventIdFor(raw.event_id),
        eventId: raw.event_id,
        clientId: raw.client_id,
        eventType: raw.event_type,
        payload: { content: raw.content },
        state: toEventState(raw.delivery_status),
        retryCount: 0,
        lastError:
          toEventState(raw.delivery_status) === 'FAILED' ? 'Webhook returned 500' : null,
        createdAt: timestamp,
        updatedAt: timestamp,
      });
    });

    return this.cache;
  }

  private async readFixture(): Promise<FixtureFile> {
    const raw = await readFile(this.fixturePath, 'utf8');
    const parsed: unknown = JSON.parse(raw);

    if (
      typeof parsed !== 'object' ||
      parsed === null ||
      !Array.isArray((parsed as FixtureFile).events)
    ) {
      throw new Error(`${this.fixturePath} does not contain an "events" array`);
    }

    return parsed as FixtureFile;
  }
}
