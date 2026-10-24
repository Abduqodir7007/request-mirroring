# `.env.shadow` checklist

Start from a copy of the production env (`cp .env.prod .env.shadow; chmod 600 .env.shadow`), then go through every line. Names below are typical — search the new service's config loader for the real ones and for hard-coded defaults.

| group | variables (typical) | shadow value |
|---|---|---|
| Database | `DATABASE_URL`, `DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` | `shadow_pg`, the shadow DB name, shadow user |
| Redis / cache | `REDIS_URL`, `REDIS_HOST`, `CACHE_URL`, `CELERY_BROKER_URL` | `shadow_redis` |
| Message broker | `RABBITMQ_*`, `KAFKA_*`, `AMQP_URL` | empty **only if the code treats empty as disabled**, otherwise a separate vhost |
| Object storage | `S3_BUCKET`, `MEDIA_BUCKET`, `MINIO_BUCKET` | `<bucket>-shadow` (create it) |
| Telegram | `TELEGRAM_API_URL` (base URL) | `http://shadow_fake:8081` |
| Bot tokens / webhooks | `*_BOT_TOKEN`, `WEBHOOK_BASE_URL`, `SET_WEBHOOK` | tokens may stay (fake answers) but `setWebhook` must never reach Telegram; disable auto-registration |
| AI / LLM | `AI_API_URL`, `OPENAI_BASE_URL`, `OPENROUTER_*`, `AI_SECRET_KEY` | `http://shadow_fake:8081/ai/v1/chat/completions`, dummy key |
| Outgoing webhooks | `WEBHOOK_URL`, `CALLBACK_URL`, `NOTIFY_URL` | `http://shadow_fake:8081/sink/<name>` — **never empty** |
| Identity / KYC | `MYID_*`, `OAUTH_*` provider URLs | `http://shadow_fake:8081/myid` (503) |
| SMS / e-mail / push | `SMS_*`, `SMTP_*`, `FCM_*` | fake `/sink/` or disabled |
| Payments | `PAYME_*`, `CLICK_*`, `STRIPE_*` | fake or disabled |
| Internal services (gRPC/HTTP) | `*_GRPC_HOST`, `*_SERVICE_URL` | their shadow, or disabled |
| Schedulers | `SCHEDULER_ENABLED`, cron flags | off until all of the above are fakes, then on |
| Feature flags | business flags | **same values as the primary**, otherwise every response differs |
| Secrets for JWT | `SECRET_KEY`, `JWT_*` | same as primary if tokens issued by the primary must validate on the shadow |

Final check before `mirror-switch on`:

```bash
grep -E '^[A-Z0-9_]+=' .env.shadow | cut -d= -f1 > /tmp/keys        # names only, no values on screen
grep -nE '=(https?://|[a-z0-9_.-]+:[0-9]+)' .env.shadow | sed -E 's#://[^@/]*@#://***@#'   # every host, credentials hidden
grep -nE '^[A-Z0-9_]*(URL|HOST|ENDPOINT)=$' .env.shadow             # empty URLs → must be none
```
