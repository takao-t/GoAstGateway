<?php
require_once 'lib/TokenProvider.php';

// **注意: オーディオを扱うためhttps/wss接続が必須です **
// ローカル(LAN)上で使用する場合でもローカルな認証証書を作成し各ブラウザに配布してください

// 設定(CRM等にログイン時に取得する想定)
$jwt_secret = "CHANGE_THIS_TO_A_VERY_LONG_RANDOM_STRING";
// ポート番号はCRMから取得するかハードコードしてGAGにあわせる
$wss_url = "wss://" . $_SERVER['SERVER_NAME'] . ":8766/phone";
// JWTのトークン生成
$tokenProvider = new TokenProvider($jwt_secret);

if (isset($_GET['action']) && $_GET['action'] === 'get_token') {
    $ext = $_GET['ext'] ?? '2001';
    echo $tokenProvider->generateToken($ext);
    exit;
}
// 内線番号もCRMログイン時に取得する想定
$default_ext = '2001';
?>
<!DOCTYPE html>
<html lang="ja">
<head>
    <meta charset="UTF-8">
    <title>Asterisk Web Phone (SDK Demo)</title>
    <link href="css/style.css" rel="stylesheet">
    <style>
        .status-dot { height: 10px; width: 10px; border-radius: 50%; display: inline-block; }
        .bg-connected { background-color: #28a745; }
        .bg-disconnected { background-color: #dc3545; }
    </style>
</head>
<body>
<div class="container mt-5">
    <div class="card" style="max-width: 500px; margin: 0 auto;">
        <div class="card-header bg-dark text-white">Web Phone Component</div>
        <div class="card-body">
            <div class="input-group mb-3">
                <span class="input-group-text">Ext</span>
                <input type="text" id="extInput" class="form-control" value="<?php echo htmlspecialchars($default_ext); ?>">
                <button id="btnConnect" class="btn btn-success">Login</button>
                <button id="btnDisconnect" class="btn btn-outline-danger" disabled>Logout</button>
            </div>
            <div class="text-center mb-4">
                <span id="statusDot" class="status-dot bg-disconnected"></span>
                <span id="statusText">オフライン</span>
            </div>
            <div class="d-grid gap-2">
                <div id="incomingAlert" class="alert alert-warning d-none">
                    着信中... <br>
                    <button id="btnAnswer" class="btn btn-primary w-100 mt-2">応答 (Answer)</button>
                </div>
                <button id="btnHangup" class="btn btn-danger" disabled>切断 (Hangup)</button>
            </div>
        </div>
    </div>
</div>

<script type="module">
    import { WebPhone } from './js/WebPhone.js';

    const wssUrl = "<?php echo $wss_url; ?>";
    let phone = null;

    const btnConnect = document.getElementById('btnConnect');
    const btnDisconnect = document.getElementById('btnDisconnect');
    const btnAnswer = document.getElementById('btnAnswer');
    const btnHangup = document.getElementById('btnHangup');
    const statusText = document.getElementById('statusText');
    const statusDot = document.getElementById('statusDot');
    const incomingAlert = document.getElementById('incomingAlert');

    btnConnect.addEventListener('click', async () => {
        const ext = document.getElementById('extInput').value;
        const res = await fetch(`?action=get_token&ext=${ext}`);
        const token = await res.text();

        // SDKの初期化
        phone = new WebPhone({
            wsUrl: wssUrl,
            token: token
        });

        phone.on('onConnect', () => {
            statusText.textContent = "待機中 (IDLE)";
            statusDot.className = "status-dot bg-connected";
            btnConnect.disabled = true;
            btnDisconnect.disabled = false;
        });

        phone.on('onDisconnect', () => {
            statusText.textContent = "切断";
            statusDot.className = "status-dot bg-disconnected";
            btnConnect.disabled = false;
            btnDisconnect.disabled = true;
            btnHangup.disabled = true;
            incomingAlert.classList.add('d-none');
        });

        phone.on('onRing', () => {
            statusText.textContent = "着信中...";
            incomingAlert.classList.remove('d-none');
        });

        phone.on('onHangup', (reason) => {
            statusText.textContent = reason === "BUSY" ? "話し中" : "通話終了/待機";
            btnHangup.disabled = true;
            incomingAlert.classList.add('d-none');
        });

        phone.on('onError', (err) => {
            console.error(err);
            alert("エラー: " + err);
        });

        await phone.connect();
    });

    btnDisconnect.addEventListener('click', () => { if (phone) phone.disconnect(); });
    btnAnswer.addEventListener('click', () => {
        if (phone) {
            phone.answer();
            incomingAlert.classList.add('d-none');
            btnHangup.disabled = false;
            statusText.textContent = "通話中";
        }
    });
    btnHangup.addEventListener('click', () => { if (phone) phone.hangup(); });
</script>
</body>
</html>
