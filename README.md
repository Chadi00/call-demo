# Call Demo - Clean Architecture Twilio Voice Recording Service

A minimal Go service with clean architecture that handles Twilio voice calls and uploads recordings to Supabase.

## Features

- Clean separation of concerns with focused packages
- Configuration via environment variables or command flags
- Uses official Twilio and Supabase Go SDKs (no manual HTTP calls)
- Answer incoming calls via Twilio webhooks
- Record calls automatically  
- Upload recordings to Supabase storage
- Validate Twilio webhook signatures

## Quick Start

1. **Setup environment variables:**
```bash
cp env.example .env
# Edit .env with your credentials
```

2. **Run the service:**
```bash
go run main.go
```

3. **Or with flags:**
```bash
go run main.go -port=8080 -twilio-sid=AC123... -twilio-token=abc123...
```

4. **Configure Twilio webhook:**
Set your Twilio number's voice webhook to: `https://your-domain.com/twilio/voice`

## Configuration

Environment variables or command line flags:

```bash
TWILIO_ACCOUNT_SID / -twilio-sid     # Your Twilio Account SID
TWILIO_AUTH_TOKEN / -twilio-token    # Your Twilio Auth Token
PORT / -port                         # HTTP server port (default: 8080)
SUPABASE_URL / -supabase-url         # Your Supabase project URL
SUPABASE_SERVICE_ROLE_KEY / -supabase-key  # Supabase service role key
SUPABASE_BUCKET / -supabase-bucket   # Storage bucket (default: voice-recording)
```

## Architecture

Clean, focused packages with single responsibilities:

```
call-demo/
├── main.go           # Composition root - wires everything together
├── config/           # Configuration loading (env vars + flags)
│   └── config.go
├── twilio/           # Twilio domain - webhooks, auth, API calls
│   └── twilio.go
├── supabase/         # Storage domain - file uploads
│   └── storage.go
├── go.mod
└── env.example
```

### Package Responsibilities

- **`main.go`**: High-level composition, dependency injection, HTTP server setup
- **`config/`**: Load and validate configuration from environment/flags
- **`twilio/`**: All Twilio-related functionality (webhooks, auth, recording API)
- **`supabase/`**: Storage operations and Supabase API interactions

## API Endpoints

- `POST /twilio/voice` - Main voice webhook (handles incoming calls)
- `POST /twilio/recording-status` - Recording completion callback
- `GET /health` - Health check

## How It Works

1. Incoming call triggers `/twilio/voice` webhook in `twilio` package
2. Twilio service validates signature and starts call recording
3. Returns TwiML response to handle the call
4. When recording completes, Twilio calls `/twilio/recording-status`
5. Twilio service downloads recording and uploads via `supabase` package