# call-demo

A WebRTC transcription server using Pion and AssemblyAI. It now supports both legacy HTTP SDP exchange and a WebSocket realtime signaling flow (trickle ICE) for near-instant connections.

## Setup

1. **Get AssemblyAI API Key:**
   - Sign up at [AssemblyAI](https://www.assemblyai.com/)
   - Get your API key from the dashboard

2. **Set Environment Variables:**
   Put these in `.env` or export them:
   ```bash
   export HTTP_ADDRESS=":8080"                # optional, defaults to :8080
   export ASSEMBLYAI_API_KEY="your_api_key_here"
   export CEREBRAS_API_KEY="your_cerebras_key"
   export CEREBRAS_MODEL_ID="llama-4-maverick-17b-128e-instruct"
   export DEEPGRAM_API_KEY="your_deepgram_key"
   export DEEPGRAM_TTS_MODEL="aura-2-thalia-en" # optional; defaults to aura-2-thalia-en
   export AUTH_PASSWORD="change-me"           # optional; WS signaling password
   export ICE_SERVERS_JSON='[{"urls":["stun:stun.l.google.com:19302"]}]' # optional
   ```

3. **Run the Server:**
   ```bash
   go run ./cmd/server
   ```

## API

- POST `/call` (legacy, no trickle ICE)
  - Request body:
    ```json
    { "type": "offer", "sdp": "..." }
    ```
  - Response body:
    ```json
    { "type": "answer", "sdp": "..." }
    ```

- WebSocket `/realtime` (preferred)
  - Connect: `ws://localhost:8080/realtime`
  - Optionally authenticate first message: `{ "type": "auth", "password": "..." }` (or send `Authorization: Bearer ...` header)
  - Send offer: `{ "type": "offer", "sdp": "..." }`
  - Receive answer: `{ "type": "answer", "sdp": "..." }`
  - Trickle ICE to server: `{ "type": "candidate", "candidate": "...", "sdpMid": "0", "sdpMLineIndex": 0 }`
  - Receive server candidates mirror format and `{"type":"ice-complete"}` when done

## How It Works

1. **WebRTC Connection:** Client establishes peer connection for audio streaming
2. **Audio Capture:** Server receives Opus-encoded audio via RTP packets  
3. **Transcription:** Audio is streamed to AssemblyAI for real-time transcription
4. **Data Channel:** Live transcripts are sent back via WebRTC data channels

## Demo

Open `demo-web/index.html`, set the signaling URL to `ws://localhost:8080/realtime`, and optionally fill the password.

## Notes

- `/realtime` uses trickle ICE and answers immediately to reduce time-to-first-audio.
- Configure `ICE_SERVERS_JSON` with your TURN for reliable NAT traversal in production.
- Add proper origin checks/CORS and rotate `AUTH_PASSWORD` in production.
