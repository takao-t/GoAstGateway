// js/WebPhone.js
import { AudioEngine } from './AudioEngine.js';

export class WebPhone {
    constructor(config) {
        this.wsUrl = config.wsUrl;
        this.token = config.token;
        this.audioEngine = new AudioEngine();
        this.ws = null;
        
        this.callbacks = {
            onConnect: () => {},
            onDisconnect: () => {},
            onRing: () => {},
            onHangup: () => {},
            onError: (err) => {}
        };
    }

    on(event, callback) {
        if (this.callbacks[event] !== undefined) {
            this.callbacks[event] = callback;
        }
    }

    async connect() {
        try {
            await this.audioEngine.init((pcmData) => {
                if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                    this.ws.send(pcmData);
                }
            });

            const url = `${this.wsUrl}?token=${this.token}`;
            this.ws = new WebSocket(url);
            this.ws.binaryType = 'arraybuffer';

            this.ws.onopen = () => this.callbacks.onConnect();
            this.ws.onclose = () => {
                this.cleanup();
                this.callbacks.onDisconnect();
            };
            this.ws.onerror = (e) => this.callbacks.onError(e);
            this.ws.onmessage = (event) => this.handleMessage(event);

        } catch (e) {
            this.callbacks.onError(e);
        }
    }

    handleMessage(event) {
        if (event.data instanceof ArrayBuffer) {
            this.audioEngine.playRemoteAudio(event.data);
        } else {
            const msg = event.data;
            switch (msg) {
                case "RINGING":
                    this.audioEngine.playRingtone();
                    this.callbacks.onRing();
                    break;
                case "HANGUP":
                case "BUSY":
                case "KICKED":
                    this.audioEngine.stopRingtone();
                    this.audioEngine.stopMic();
                    this.callbacks.onHangup(msg);
                    break;
                case "ANSWER":
                    this.audioEngine.stopRingtone();
                    this.audioEngine.startMic();
                    break;
            }
        }
    }

    answer() {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send("ANSWER");
            this.audioEngine.stopRingtone();
            this.audioEngine.startMic();
        }
    }

    hangup() {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send("HANGUP");
        }
        this.audioEngine.stopRingtone();
        this.audioEngine.stopMic();
    }

    disconnect() {
        if (this.ws) this.ws.close();
        this.cleanup();
    }

    cleanup() {
        this.audioEngine.close();
        this.ws = null;
    }
}
