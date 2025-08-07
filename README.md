# Secure Twilio Voice Webhook

This Go application provides a secure Twilio voice webhook with authentication and phone number filtering.

## Security Features

### 1. Twilio Signature Validation
- Validates that incoming requests are actually from your Twilio account using HMAC-SHA1 signature verification
- Prevents unauthorized requests from spoofed sources
- Accepts calls from any phone number, but only processes requests verified to come from Twilio

## Configuration

### Required Environment Variables

```bash
# Twilio
TWILIO_ACCOUNT_SID=your_account_sid_here
TWILIO_AUTH_TOKEN=your_twilio_auth_token_here

# Supabase
SUPABASE_URL=https://your-supabase-project.supabase.co
SUPABASE_SERVICE_ROLE_KEY=your_service_role_key_here

# Server
PORT=8080
# Strongly recommended in production so callbacks use a public absolute URL
BASE_URL=https://your-public-domain.example.com
```

### Optional Environment Variables

```bash
# Server port
PORT=8080
```

## Running the Application

1. Set your environment variables:
```bash
export TWILIO_ACCOUNT_SID="ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
export TWILIO_AUTH_TOKEN="your_twilio_auth_token"
export SUPABASE_URL="https://your-supabase-project.supabase.co"
export SUPABASE_SERVICE_ROLE_KEY="your_service_role_key"
export PORT="8080"
export BASE_URL="https://your-public-domain.example.com"
```

2. Run the application:
```bash
go run ./cmd/server
```

## Endpoints

- `GET /healthz` - Health check endpoint (no authentication required)
- `POST /twilio/voice` - Twilio voice webhook (requires Twilio signature validation). Starts a single continuous recording via REST API.
- `POST /twilio/recording-status` - Recording status callback to upload recording to Supabase when complete
 - `POST /twilio/key-pressed` - DTMF demo handler while recording continues
 - `POST /twilio/wait-for-input` - Idle loop waiting for DTMF
 - `POST /twilio/recording-complete` - Simple thank-you TwiML
 - `POST /twilio/voice-conversation` - Demo endpoint for conversation recording
 - `POST /twilio/dial-complete` - Post-dial status handler

## Security Behavior

### Valid Request Flow:
1. Request arrives at `/twilio/voice`
2. Middleware validates Twilio signature using `TWILIO_AUTH_TOKEN`
3. If signature validation passes, the request proceeds to the handler
4. Handler responds with personalized TwiML including the caller's number

### Invalid Request Responses:
- **Invalid signature**: `401 Unauthorized - "Invalid Twilio signature"`
- **Missing configuration**: `500 Internal Server Error - "TWILIO_AUTH_TOKEN not configured"`
- **Malformed request**: `400 Bad Request - "Failed to parse form data"`

## Logging

The application logs all calls with the format:
```
Call from +1234567890 to +0987654321
```

## Production Considerations

1. **Always set `TWILIO_AUTH_TOKEN`** - This is required for security
2. **Use HTTPS** - Ensure your webhook URL uses HTTPS in production
3. **Environment Security** - Store sensitive environment variables securely
4. **Monitoring** - Monitor logs for unauthorized access attempts

## Testing

You can test the webhook using Twilio's webhook testing tools or by configuring it as your Twilio phone number's voice webhook URL.

Make sure your webhook URL in Twilio console is set to: `https://yourdomain.com/twilio/voice`

## Project Structure

```
cmd/
  server/
    main.go
internal/
  config/
    config.go
  httpserver/
    router.go
  infra/
    storage/
      supabase.go
  middleware/
    twilio_sig.go
  usecase/
    twilio.go
api/
  http/
    handlers.go
```
