# SplittyPie Reference (Implementation-Oriented)

## 1) Product purpose and core workflows

SplittyPie is an offline-first expense splitting app (Ember PWA) for creating shared events/trips, adding participants and expenses, and calculating settlements.
Sources: [README.md](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/README.md), [app/templates/index.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/index.hbs), [config/environment.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/config/environment.js).

Core flow (from routes/templates/tests):

1. Create event (`/new`) with name, currency, users ([app/router.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/router.js), [app/routes/new.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/new.js), [app/templates/components/event-form.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/components/event-form.hbs)).
2. On first event visit, pick identity (“who you are”) ([app/routes/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event.js), [app/routes/event/who-are-you.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event/who-are-you.js), [app/templates/event/who-are-you.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/event/who-are-you.hbs)).
3. Add expenses via full form or “Quick Add” parser modal ([app/routes/event/transactions/new.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event/transactions/new.js), [app/components/quick-add-form.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/components/quick-add-form.js), [app/utils/parse-transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/utils/parse-transaction.js)).
4. View balances + suggested settlement transfers and convert a suggestion into a transfer transaction ([app/templates/event/index.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/event/index.hbs), [app/components/settlement-transfer-list.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/components/settlement-transfer-list.js), [app/routes/event/index.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event/index.js)).
5. Share event by URL, without signup/auth in normal flow ([app/models/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/event.js), [app/routes/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event.js)).

## 2) Information architecture / screens / navigation

Route map:

- `/` index (landing + previous events)
- `/new` create event
- `/:event_id` event shell
  - `/:event_id` overview
  - `/:event_id/transactions`
  - `/:event_id/transactions/new`
  - `/:event_id/:transaction_id` edit transaction
  - `/:event_id/edit`
  - `/:event_id/who-are-you`
- wildcard not-found
  Source: [app/router.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/router.js).

Global shell uses side menu + navbar + modal outlet; events list and “New Event” are globally accessible from side menu ([app/templates/application.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/application.hbs), [app/routes/application.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/application.js)).

## 3) Domain model (inferred)

- **Event**: `name`, `isOffline`, `currency`, `users[]`, `transactions[]`, computed `url` ([app/models/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/event.js)).
- **User**: `name`, belongs to `event`, computed `balance` from event transactions ([app/models/user.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/user.js)).
- **Transaction**: `name`, `amount`, `date`, `event`, `payer`, `participants[]`, `type` (`expense` default, `transfer`), computed `month`, `isTransfer` ([app/models/transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/transaction.js)).
- **Currency**: static fixture list ([app/models/currency.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/currency.js)).
- **SyncJob**: queued operation (`name`, `payload`, `createdAt`) ([app/models/sync-job.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/models/sync-job.js)).

Event payloads embed users + transactions in serialization ([app/serializers/online/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/serializers/online/event.js), [app/serializers/offline/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/serializers/offline/event.js)).

## 4) State management and data flow patterns

Pattern: route-driven model loading + service/repository orchestration.

- Repositories (`eventRepository`, `transactionRepository`) save local-first and enqueue sync jobs ([app/repositories/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/repositories/event.js), [app/repositories/transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/repositories/transaction.js)).
- `syncQueue` persists and processes jobs FIFO when online ([app/services/sync-queue.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/sync-queue.js)).
- `jobProcessor` maps job names to online-store commands ([app/services/job-processor.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/job-processor.js)).
- `syncer` handles full sync, conflict behavior, and realtime updates ([app/services/syncer.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/syncer.js)).
- `connection` tracks browser online/offline state ([app/services/connection.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/connection.js)).

Identity context is stored per event in local storage using `event-<id>-current-user` ([app/services/user-context.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/user-context.js)).

## 5) Persistence / backend / API interactions

- Online persistence: Firebase Realtime DB via EmberFire online adapter ([app/adapters/online/application.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/adapters/online/application.js), [app/services/online-store.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/online-store.js)).
- Offline persistence: localForage adapter + generated local IDs ([app/adapters/offline/application.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/adapters/offline/application.js), [app/services/store.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/store.js)).
- Offline transaction adapter no-ops create/update (transactions are written through event payload updates) ([app/adapters/offline/transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/adapters/offline/transaction.js)).
- Firebase hosting rewrites all routes to SPA entry and DB rules are permissive (`.read/.write: true` under `events`) ([firebase.json](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/firebase.json), [firebase_rules.json](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/firebase_rules.json)).

## 6) Key technical architecture decisions

- Ember 2.18-era app using Ember Data + EmberFire + localforage adapter + liquid-fire + side-menu + cp-validations ([package.json](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/package.json)).
- PWA/offline support via service worker registration and environment config ([app/initializers/offline-support.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/initializers/offline-support.js), [config/environment.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/config/environment.js)).
- Build includes Sass/SVG tooling and analytics/error-reporting hooks ([ember-cli-build.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/ember-cli-build.js), [config/environment.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/config/environment.js)).

## 7) UX patterns worth reusing

1. **Quick Add natural-text parsing** with live preview and branch to full details flow ([app/components/quick-add-form.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/components/quick-add-form.js), [app/templates/components/quick-add-form.hbs](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/templates/components/quick-add-form.hbs), [app/utils/parse-transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/utils/parse-transaction.js)).
2. **Per-event identity switching** (“viewing as …”) without account friction ([app/routes/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event.js), [app/services/user-context.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/user-context.js), [tests/acceptance/event-test.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/tests/acceptance/event-test.js)).
3. **Settlement suggestions** and one-click conversion into transfer transactions ([app/components/settlement-transfer-list.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/components/settlement-transfer-list.js), [app/routes/event/index.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event/index.js)).
4. **Offline-first UX feedback** for sync progress, updates, and conflict notices ([app/routes/event.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/event.js), [app/initializers/offline-support.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/initializers/offline-support.js), [app/routes/application.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/application.js)).

## 8) Risks / unknowns and prototyping needs

- **Security model risk**: Firebase rules are open; trust relies on unguessable URLs/social trust, not auth ([firebase_rules.json](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/firebase_rules.json)).
- **Aging stack risk**: architecture ideas should be ported conceptually to modern stack, not copied framework-level ([package.json](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/package.json)).
- **Conflict semantics gap**: only some conflict behaviors are explicit and should be redefined in your product rules ([app/services/syncer.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/syncer.js), [app/routes/application.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/routes/application.js)).

Prototype first:

1. Offline job queue + replay ordering.
2. Dual-store reconciliation behavior.
3. Quick-add parser accuracy.
4. Settlement algorithm correctness on edge cases.
   Sources: [app/services/sync-queue.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/sync-queue.js), [app/services/job-processor.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/services/job-processor.js), [app/utils/parse-transaction.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/utils/parse-transaction.js), [app/components/settlement-transfer-list.js](https://github.com/tsubik/splittypie/blob/da052e64bef2498cc832da521562e3d0d6c49f9c/app/components/settlement-transfer-list.js).
