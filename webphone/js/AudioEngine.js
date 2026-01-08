export class AudioEngine {
    constructor() {
        this.ctx = null;
        this.workletNode = null;
        this.sourceNode = null;
        this.mediaStream = null;
        // this.ringtoneCtx = null; // 削除: 別のコンテキストは作らない
        this.cadenceInterval = null;
        this.oscillators = [];
    }

    async init(onMicDataCallback) {
        // 既にコンテキストがある場合は再利用、なければ作成
        if (!this.ctx) {
            this.ctx = new (window.AudioContext || window.webkitAudioContext)({ sampleRate: 16000 });
        } else if (this.ctx.state === 'suspended') {
            await this.ctx.resume();
        }

        // Workletの多重読み込み防止
        try {
            const workletUrl = this.createWorkletBlobUrl();
            await this.ctx.audioWorklet.addModule(workletUrl);
            
            this.workletNode = new AudioWorkletNode(this.ctx, 'pcm-processor');
            this.workletNode.connect(this.ctx.destination);
            
            this.workletNode.port.onmessage = (e) => {
                const pcmInt16 = this.convertFloat32ToInt16(e.data);
                if (onMicDataCallback) onMicDataCallback(pcmInt16.buffer);
            };
        } catch (e) {
            console.error("AudioWorklet setup failed:", e);
        }
    }

    createWorkletBlobUrl() {
        const code = `
            class PCMProcessor extends AudioWorkletProcessor {
                constructor() {
                    super();
                    this.buffer = []; this.inputBuffer = []; this.CHUNK_SIZE = 320;
                }
                process(inputs, outputs, parameters) {
                    const output = outputs[0];
                    const channel0 = output[0];
                    const channel1 = output[1]; // 出力がステレオの場合への保険
                    
                    // --- 受信音声の再生処理 ---
                    for (let i = 0; i < channel0.length; i++) {
                        let sample = 0;
                        if (this.buffer.length > 0) sample = this.buffer.shift();
                        channel0[i] = sample;
                        if (channel1) channel1[i] = sample;
                    }

                    // --- マイク音声の送信処理 ---
                    const input = inputs[0];
                    if (input && input.length > 0) {
                        const inputChannel = input[0];
                        for (let i = 0; i < inputChannel.length; i++) {
                            this.inputBuffer.push(inputChannel[i]);
                        }
                        if (this.inputBuffer.length >= this.CHUNK_SIZE) {
                            const chunk = this.inputBuffer.slice(0, this.CHUNK_SIZE);
                            this.inputBuffer = this.inputBuffer.slice(this.CHUNK_SIZE);
                            this.port.postMessage(chunk);
                        }
                    }
                    return true;
                }
            }
            registerProcessor('pcm-processor', class extends PCMProcessor {
                constructor() {
                    super();
                    this.port.onmessage = (e) => {
                        const float32Array = e.data;
                        for (let i = 0; i < float32Array.length; i++) { this.buffer.push(float32Array[i]); }
                        // バッファ溢れ防止の安全策
                        if (this.buffer.length > 8000) { this.buffer = this.buffer.slice(-3200); }
                    };
                }
            });
        `;
        return URL.createObjectURL(new Blob([code], { type: 'application/javascript' }));
    }

    async startMic() {
        if (!this.ctx) throw new Error("AudioEngine not initialized");
        
        // Resume context if suspended (important for Autoplay policy)
        if (this.ctx.state === 'suspended') {
            await this.ctx.resume();
        }

        try {
            this.mediaStream = await navigator.mediaDevices.getUserMedia({
                audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 }
            });
            this.sourceNode = this.ctx.createMediaStreamSource(this.mediaStream);
            this.sourceNode.connect(this.workletNode);
        } catch (e) {
            console.error("Mic access denied or failed:", e);
            throw e;
        }
    }

    stopMic() {
        if (this.sourceNode) { 
            this.sourceNode.disconnect(); 
            this.sourceNode = null; 
        }
        if (this.mediaStream) { 
            this.mediaStream.getTracks().forEach(track => track.stop()); 
            this.mediaStream = null; 
        }
    }

    playRemoteAudio(arrayBuffer) {
        if (!this.workletNode) return;
        const int16Data = new Int16Array(arrayBuffer);
        const floatData = this.convertInt16ToFloat32(int16Data);
        this.workletNode.port.postMessage(floatData);
    }

    // close()は「アプリ終了時」や「ログアウト時」のみ呼ぶ想定
    close() {
        this.stopMic();
        this.stopRingtone();
        if (this.workletNode) {
            this.workletNode.disconnect();
            this.workletNode = null; // GC対象に
        }
        if (this.ctx) { 
            this.ctx.close(); 
            this.ctx = null; 
        }
    }

    convertInt16ToFloat32(int16Array) {
        const float32 = new Float32Array(int16Array.length);
        for (let i = 0; i < int16Array.length; i++) float32[i] = int16Array[i] / 32768.0;
        return float32;
    }

    convertFloat32ToInt16(float32Array) {
        const int16 = new Int16Array(float32Array.length);
        for (let i = 0; i < float32Array.length; i++) {
            let s = Math.max(-1, Math.min(1, float32Array[i]));
            int16[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
        }
        return int16;
    }

    // --- Ringtone Logic (Refactored) ---

    playRingtone() {
        // メインコンテキストを利用。もし無ければガード
        if (!this.ctx) {
            console.warn("AudioContext not initialized, cannot play ringtone.");
            return;
        }
        
        // 既に鳴っている場合は重複再生しない
        if (this.oscillators.length > 0) return;

        // 音声通話用と同じコンテキスト(16kHz)上でオシレーターを作成
        const tCtx = this.ctx; 

        const carrier = tCtx.createOscillator(); carrier.frequency.value = 400;
        const modulator = tCtx.createOscillator(); modulator.frequency.value = 17;
        const amGain = tCtx.createGain(); amGain.gain.value = 0.5;
        const depthGain = tCtx.createGain(); depthGain.gain.value = 0.5;
        const masterGain = tCtx.createGain(); masterGain.gain.value = 0;

        modulator.connect(depthGain).connect(amGain.gain);
        carrier.connect(amGain).connect(masterGain).connect(tCtx.destination);

        carrier.start();
        modulator.start();

        this.oscillators = [carrier, modulator];

        const schedule = () => {
            const now = tCtx.currentTime;
            // 呼び出し音が「プルルル」となるようなエンベロープ
            masterGain.gain.cancelScheduledValues(now);
            masterGain.gain.setValueAtTime(0, now);
            masterGain.gain.linearRampToValueAtTime(0.5, now + 0.1);
            masterGain.gain.linearRampToValueAtTime(0.5, now + 1.0);
            masterGain.gain.linearRampToValueAtTime(0, now + 1.1);
        };

        schedule();
        this.cadenceInterval = setInterval(() => {
            if (tCtx.state === 'running') schedule();
        }, 3000);
    }

    stopRingtone() {
        if (this.cadenceInterval) {
            clearInterval(this.cadenceInterval);
            this.cadenceInterval = null;
        }
        // オシレーターのみ停止・切断し、コンテキスト(this.ctx)は維持する
        this.oscillators.forEach(o => {
            try { 
                o.stop(); 
                o.disconnect(); // 明示的に切断してGCを促す
            } catch(e) {}
        });
        this.oscillators = [];
        
        // ここで this.ctx.close() は行わない！
    }
}
