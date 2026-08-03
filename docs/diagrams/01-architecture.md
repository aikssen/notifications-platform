# System architecture

Everything in this diagram exists and runs. Nothing is aspirational.

```mermaid
flowchart TB
  subgraph public["Public network"]
    webui["Client application"]
    ops["Operations console"]
    hook["Client webhook<br/>(HTTPS, HMAC-signed)"]
  end

  subgraph edge["API edge — JWT, rate limit, schema validation"]
    self["self-service-api<br/><i>Node · Express</i><br/>list · detail · replay"]
    subs["subscription-service<br/><i>Node · Express</i><br/>CRUD · resolve · SSRF guard"]
    mon["monitor-service<br/><i>Node · Express</i><br/>SSE dashboard"]
  end

  subgraph private["Private network"]
    platform["Platform microservices<br/>payments · transfers · balances"]

    subgraph kafka["Apache Kafka"]
      t1["notifications.dispatch<br/>3 partitions, keyed by client"]
      t2["notifications.delivery-result"]
      t3["notifications.dlq"]
    end

    disp["dispatch-service<br/><b>Go</b><br/>consume · resolve · deliver · record"]
    retry["retry-service<br/><b>Go</b><br/>backoff · exhaust · reclaim"]
    db[("PostgreSQL<br/>events + attempts<br/>+ subscriptions")]
  end

  subgraph obs["Observability"]
    prom["Prometheus"]
    graf["Grafana"]
  end

  platform -->|"1 publish"| t1
  t1 -->|"2 consume<br/>manual offset commit"| disp
  disp -->|"3 is this deliverable?<br/>client_id + event_type"| subs
  disp -->|"4 POST, signed"| hook
  disp -->|"5 attempt + state<br/>one transaction"| db
  disp -->|"6 outcome"| t2

  db -.->|"7 what is due?<br/>SKIP LOCKED"| retry
  retry -->|"8 requeue<br/>RETRY_SERVICE"| t1
  retry -->|"9 budget spent"| t3
  retry -->|"FAILED"| db

  webui -->|HTTPS + JWT| self
  webui -->|HTTPS + JWT| subs
  self -->|read| db
  self -->|"replay<br/>SELF_SERVICE"| t1
  subs --> db

  t2 --> mon
  ops -->|SSE| mon
  mon -.->|"replay via the public API"| self

  disp -.->|/metrics| prom
  retry -.->|/metrics| prom
  self -.->|/metrics| prom
  subs -.->|/metrics| prom
  prom --> graf

  classDef go fill:#00ADD8,stroke:#007d9c,color:#fff
  classDef node fill:#539E43,stroke:#3c7a30,color:#fff
  classDef store fill:#8b5cf6,stroke:#6d28d9,color:#fff
  classDef bus fill:#f59e0b,stroke:#b45309,color:#fff
  class disp,retry go
  class self,subs,mon node
  class db store
  class t1,t2,t3 bus
```

## The three properties worth defending

**One delivery pipeline.** A first delivery (1→2), an automatic retry (8→2) and
a client replay (self-service→2) all enter through the same topic and run the
same code. They differ only in `dispatch_source`, which exists so the audit
trail can tell them apart. There is no second implementation of delivery that
could drift from the first.

**The producer knows nothing.** `platform` publishes an event. It does not know
webhooks exist, which clients are subscribed, or whether a delivery failed. Add
a delivery channel tomorrow and no producer changes.

**Observability is a consumer.** `monitor-service` subscribes to `t2`. It holds
no database connection and nothing in the delivery path references it. Remove
it and delivery is unaffected.

## Where the boundaries are

| Boundary | Enforced by |
|---|---|
| Client ↔ platform | JWT at the edge; `client_id` from the token, never the request |
| Notifications ↔ Subscriptions | HTTP call on `(client_id, event_type)` — two bounded contexts |
| Platform ↔ client endpoint | SSRF guard, HMAC signature, no redirects, egress timeout |
| Delivery ↔ retry policy | dispatch answers *did it work*; retry answers *try again?* |
| Delivery ↔ observability | a Kafka topic, one-way |
