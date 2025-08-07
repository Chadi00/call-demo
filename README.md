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
# Your Twilio Auth Token (required for signature validation)
TWILIO_AUTH_TOKEN=your_twilio_auth_token_here
```

### Optional Environment Variables

```bash
# Server port
PORT=8080
```

## Running the Application

1. Set your environment variables:
```bash
export TWILIO_AUTH_TOKEN="your_twilio_auth_token"
export PORT="8080"
```

2. Run the application:
```bash
go run main.go
```

## Endpoints

- `GET /healthz` - Health check endpoint (no authentication required)
- `POST /twilio/voice` - Twilio voice webhook (requires Twilio signature validation)

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
