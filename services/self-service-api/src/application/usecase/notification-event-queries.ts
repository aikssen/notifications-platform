import {
  DomainError,
  type NotificationEvent,
  type NotificationEventFilters,
  type Page,
} from '../../domain/notification-event.js';
import type { NotificationEventDetail, NotificationEventRepository } from '../ports.js';

/**
 * "Query for all the event notifications per client allowing to filter by
 *  event creation date and delivery_status criteria."
 *
 * Both filters are implemented. The previous version accepted only a client id
 * and returned everything, which meant a client with months of history had no
 * way to find the delivery they were asking about.
 */
export class ListNotificationEvents {
  constructor(private readonly repository: NotificationEventRepository) {}

  execute(filters: NotificationEventFilters): Promise<Page<NotificationEvent>> {
    if (filters.createdFrom && filters.createdTo && filters.createdFrom > filters.createdTo) {
      throw new DomainError('created_from is after created_to', 'INVALID_DATE_RANGE');
    }
    return this.repository.findPage(filters);
  }
}

/**
 * "Obtain a single event notification details."
 *
 * The client id is part of the lookup rather than a check applied to the
 * result. The previous implementation fetched by id alone and never compared
 * the owner, so any client could read any other client's delivery history —
 * including the payloads of their payments — by guessing an id.
 */
export class GetNotificationEventDetail {
  constructor(private readonly repository: NotificationEventRepository) {}

  execute(id: string, clientId: string): Promise<NotificationEventDetail | null> {
    return this.repository.findByIdForClient(id, clientId);
  }
}
