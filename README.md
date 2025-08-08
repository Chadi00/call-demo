# call-demo

A WebRTC transcription server using Pion and AssemblyAI with an HTTP `/call` route. The client posts an SDP offer (JSON), receives an SDP answer, and gets live transcripts via WebRTC data channels.

## Setup

1. **Get AssemblyAI API Key:**
   - Sign up at [AssemblyAI](https://www.assemblyai.com/)
   - Get your API key from the dashboard

2. **Set Environment Variables:**
   ```bash
   export ASSEMBLYAI_API_KEY="your_api_key_here"
   export HTTP_ADDRESS=":8080"  # optional, defaults to :8080
   ```

3. **Run the Server:**
   ```bash
   go run ./cmd/server
   ```

## API

- POST `/call`
  - Request body:
    ```json
    { "type": "offer", "sdp": "..." }
    ```
  - Response body:
    ```json
    { "type": "answer", "sdp": "..." }
    ```

## How It Works

1. **WebRTC Connection:** Client establishes peer connection for audio streaming
2. **Audio Capture:** Server receives Opus-encoded audio via RTP packets  
3. **Transcription:** Audio is streamed to AssemblyAI for real-time transcription
4. **Data Channel:** Live transcripts are sent back via WebRTC data channels

## Demo

A live demo is available in the `super/` project. See `super/DEMO.md` for instructions.

## Notes

- Currently sends Opus-encoded audio directly to AssemblyAI (may not work optimally)
- For production: decode Opus to PCM at 16kHz before sending to AssemblyAI
- Uses STUN servers for NAT traversal
- WebRTC data channels provide bidirectional text communication
