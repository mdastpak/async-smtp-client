# Async SMTP Client

Async SMTP Client accepts email requests over HTTP, queues them in Redis, and sends them through an SMTP server.

## Configuration

Copy `.env.sample` to `config.env` and set:

- `API_KEY`: a long, random secret sent as the `X-API-Key` header.
- `REDIS_*`: the Redis connection settings.
- `SMTP_*`: the SMTP server, username, password, and port. SMTP certificates are always verified; do not use an untrusted server certificate.
- `SERVER_HOST` and `SERVER_PORT`: the HTTP listen address.

The service refuses to start when `API_KEY` is empty. Keep `config.env` out of source control.

## Run

```text
go run .
```

Redis must be available before the service starts.

## API

Submit an email:

```text
POST /submit
X-API-Key: <API_KEY>
Content-Type: application/json

{
  "to": ["recipient@example.com"],
  "cc": [],
  "bcc": [],
  "subject": "Hello",
  "body": "Message body"
}
```

The request body is limited to 1 MiB, supports at most 100 recipients, and rejects unknown JSON fields and invalid email headers.

Check status:

```text
GET /status?uuid=<uuid>
X-API-Key: <API_KEY>
```

The `/health` endpoint is unauthenticated for service monitoring. It returns an error status when Redis or SMTP is unavailable.
