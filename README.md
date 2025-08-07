# Secure Twilio Voice Webhook

This Go application provides a secure Twilio voice webhook with authentication and phone number filtering.

## Security Features

### 1. Twilio Signature Validation
- Validates that incoming requests are actually from Twilio using HMAC-SHA1 signature verification
- Prevents unauthorized requests from spoofed sources

### 2. Phone Number Allowlist
- Restricts webhook access to specific phone numbers only
- Configurable via environment variables
- Supports various phone number formats (with/without country codes, formatting)

## Configuration

### Required Environment Variables

```bash
# Your Twilio Auth Token (required for signature validation)
TWILIO_AUTH_TOKEN=your_twilio_auth_token_here
```

### Optional Environment Variables

```bash
# Allowed phone numbers (comma-separated)
# Leave empty to allow all numbers (not recommended for production)
ALLOWED_NUMBERS=+1234567890,+0987654321

# Server port
PORT=8080
```

## Phone Number Format

The allowlist supports multiple phone number formats:
- `+1234567890` (with country code)
- `1234567890` (without country code)
- `(123) 456-7890` (with formatting)
- `123-456-7890` (with dashes)

All formats are automatically normalized for comparison.

## Running the Application

1. Set your environment variables:
```bash
export TWILIO_AUTH_TOKEN="your_twilio_auth_token"
export ALLOWED_NUMBERS="+1234567890,+0987654321"
export PORT="8080"
```

2. Run the application:
```bash
go run main.go
```

## Endpoints

- `GET /healthz` - Health check endpoint (no authentication required)
- `POST /twilio/voice` - Twilio voice webhook (requires Twilio authentication and allowed phone number)

## Security Behavior

### Valid Request Flow:
1. Request arrives at `/twilio/voice`
2. Middleware validates Twilio signature using `TWILIO_AUTH_TOKEN`
3. Middleware checks if the calling number (`From` parameter) is in the allowlist
4. If both checks pass, the request proceeds to the handler
5. Handler responds with personalized TwiML

### Invalid Request Responses:
- **Invalid signature**: `401 Unauthorized - "Invalid Twilio signature"`
- **Unauthorized phone number**: `403 Forbidden - "Phone number not authorized"`
- **Missing configuration**: `500 Internal Server Error - "TWILIO_AUTH_TOKEN not configured"`
- **Malformed request**: `400 Bad Request - "Failed to parse form data"`

## Logging

The application logs all authorized calls with the format:
```
Authorized call from +1234567890 to +0987654321
```

## Production Considerations

1. **Always set `TWILIO_AUTH_TOKEN`** - This is required for security
2. **Configure `ALLOWED_NUMBERS`** - Don't leave this empty in production
3. **Use HTTPS** - Ensure your webhook URL uses HTTPS in production
4. **Environment Security** - Store sensitive environment variables securely
5. **Monitoring** - Monitor logs for unauthorized access attempts

## Testing

You can test the webhook using Twilio's webhook testing tools or by configuring it as your Twilio phone number's voice webhook URL.

Make sure your webhook URL in Twilio console is set to: `https://yourdomain.com/twilio/voice`
