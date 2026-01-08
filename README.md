# GoAstGateway(Go Asterisk Gateway: GAG)

# これは何？
Asteriskはバージョン 23.0.0,22.6,0, 21.11.0, 20.16.0 以降で chan_websocket が使えるようになっています。これは従来までのPjSIP+Websocketではなく、Websocketでオーディオをやりとりするためのもので、端的にいうとAudiosocketをWebsocket対応にしたようなものです。ただし、Audiosocketは単純なAsteriskのapp_ですが、chan_websocketはチャネルとして動作します。つまり
```
Dial(Websocket/endpoint-identifier)
```
のようにしてDial()することができます。現在のところAsteriskのチャネルとしては『不完全』で、他のチャネルと全く同じように使えるわけではありません。ですがWebsocketによるTCPのオーディオを使って『通話』することが可能になりました。そこで、chan_websocketを使って「電話機」をつくってしまおうというのがこのプロジェクトです。

このプロジェクトではSIPを必要としません。このため基本的な呼制御のみ実装し"TCPで通話する"ことに専念します。

# しくみ
chan_websocketは着信側には簡単には使えないので、このGAGを使って『中継』を行います。システムの構成は以下のようになります。

```
+----------+                +--------------+                +-------------+
| Asterisk |---Websocket--->| GoAstGateway |<---Websocket---| Web Browser |
+----------+   (呼毎)       +--------------+     (常時)     +-------------+
```
両方の接続ともにWebsocketなので直接接続してもよさそうな感じはしますが、chan_websocketは『出』側専用なので、着信を受けるわけにはいきません。一方、ブラウザ側も『サーバ』として待ち受けるわけにはいかないので、ブラウザWebsocketの接続起点として使用します。そこでこのGAGの出番となるわけです。

Asteriskからの接続はDial()で起動されるので呼毎に行われます。一方、ブラウザからの接続はブラウザで所定のページを開いた場合等に接続され、Asteriskからの呼を待ち受けるようにします。SIPのアナロジーでいうならば、GAGはWebsocketのレジスタ・サーバとして動作することになります。Asteriskからの呼情報とブラウザの情報を照合し、マッチングさせて通話を成立させ、後は音声をパススルーする役目をGAGが担います。
ただ、これだけでは『着信』はできるけど『発信』ができないので困りますね。そこで、Asteriskのコールバック式発信を用います。
```
+----------+                +--------------+                +-------------+
| Asterisk |---Websocket--->| GoAstGateway |<---Websocket---| Web Browser |
+----------+   (呼毎)       +--------------+     (常時)     +-------------+
    |                                                              |
    +<--AMI--------------------------------------------------------+
```
例えばCRM、そんなプログラムはブラウザで動くようにPHPで書かれたりします。なのでブラウザ側からAMI(Asterisk Management Interface)を使って、Asteriskに『発信』させるのはたやすいことです。これで「コールバック式」発信ができます。
```
channel originate Local/2001@inhouse extension 0312345678@outgoing
```
のようなCLIコマンドを実行すればinhouse contextの2001に発信し、2001が応答したらoutgoing contextを使って 0312345678 にダイヤルするようなことができますのでブラウザ側は着信さえちゃんとしていれば発信もできるというわけです。

# プログラムの構成
Geminiに「ゲートウェイ・デーモンを作りたい」と相談したら「そんなんはGoで作るのがベストだ」とか言うのでGoで実装しています。Goは不慣れだったのですが、このプロジェクトでだんだんわかるようになりました。

# ビルドとインストール
githubからcloneなり何なりして入手したらGoの環境さえあれば

go build .

でコンパイルが完了します。
インストールはバイナリ(goastgateway)を /usr/local/bin にコピーしてください。それだけです。ご承知の通り、Goのバイナリは外部依存性がないので単独のファイルを配置するだけで動きます。

# 設定
設定ファイルは2つありますが、基本的には goastgateway.json だけあれば大丈夫です。デフォルトの配置では /usr/local/etc にgoastgateway.jsonを置きます。

配布時のサンプルは以下のようになっていますので、必要な個所を書き換えてください。
```
{
    "asterisk_addr": ":8765",
    "browser_addr": ":8766",
    "asterisk_format": "json",
    "extension_variable": "WS_EXTEN",
    "exten_search_pattern": "ws-ext-(\\w+)",
    "cert_file": "/usr/local/ssl/192.168.254.234.pem",
    "key_file": "/usr/local/ssl/192.168.254.234-key.pem",
    "allowed_asterisk_ips": ["127.0.0.1"],
    "allowed_browser_ips": ["127.0.0.1", "192.168.0.0/16"],
    "allowed_origins": ["https://192.168.254.234", "https://192.168.254.234:8766"],
    "jwt_secret": "CHANGE_THIS_TO_A_VERY_LONG_RANDOM_STRING",
    "log_level": "DEBUG"
}
```
- asterisk_addr : Asteriskが接続してくるアドレスとポート、先ほどの図でいうと"左側"を指定します。通常、GAGはAsteriskと同居させて使うことを想定しています。
- browser_addr : ブラウザが接続してくるアドレスとポート、先ほどの図でいうと"右側"を指定します。この例ではサーバが持っている全てのアドレスでlistenするのでアドレスは省略されています。なおGAGのエンドポイントは/phoneです。
- asterisk_format : Asteriskから送られてくるメッセージのフォーマットを指定します。指定できるのは text か json です。なお、jsonフォーマットに関してはAsteriskの特定のバージョン以降でしか使えませんので注意してください。
- extension_variable : json形式でメッセージが送られてくる場合、Asteriskのチャネル変数を渡すことができます。内線番号の識別に使うチャネル変数をここで指定します。メッセージのフォーマットが text の場合にはこの設定は意味を持ちません。
- exten_search_pattern : メッセージフォーマットが text の場合に内線を識別するための検索パターンを指定します。この例では ws-ext-2001 のように送られてきた場合には内線番号 "2001" と判断されます。
- cert_file,key_file : 右側つまりブラウザの接続では、ブラウザが音声を扱うためSSLが必要となります。LAN上で使う場合であってもローカルな証明書を作成し、配置してください。GAGはブラウザからの接続にはSSL(wss://)を必要としますのでここに証明書を書きます。
- allowed_asterisk_ips,allowed_browser_ips : いわゆるACLで、Asterisk側、ブラウザ側ともに設定できます。Asteriskは同じサーバ上で動作させるのが原則なのでlocalhost(127.0.0.1)を指定します。ブラウザ側にはLANのネットワークアドレス等を指定してください。
- allowed_origins : CRM等、GAGに接続してくるページのOriginを指定してください。
- jwt_secret : ユーザ(内線)の認証にはJWT(JASON Web Token)を使用します。このためGAG側では内線番号等の認証情報を保持しません。JWTの秘密鍵をここに記述します。
- log_level : ログ出力される情報の詳細度を指定します。

gag_groups.json はオプションの設定ファイルで、ブラウザ側の内線をグループ化するのに使用します。
```
{
  "G01": {
    "strategy": "sequential",
    "members": ["201", "202"],
    "timeout": 30
  },
  "G02": {
    "strategy": "ringall",
    "members": ["201", "202"],
    "timeout": 30
  }
```
このサンプルではG01,G02というグループを設定しています。複数のブラウザフォンを同時あるいは順次鳴動させたい場合に使用することができます。Asterisk側でDial()する際に複数鳴らしてもかまわないので、あまり必要ないかもしれませんが順次鳴動の場合に使ってください。あくまでもオプションです。
# 起動
```
goastgateway -c 設定ファイル -g グループ設定ファイル
```
設定ファイル類のデフォルトの配置場所は /usr/local/etc です。コマンドラインで指定されなかった場合には /usr/local/etc が参照されます。

systemdから起動する場合には以下のようなUnitファイルをつくってください。
```
[Unit]
Description=Go Asterisk Gateway Service
After=network.target

[Service]
# コンパイルされたバイナリのパス
ExecStart=/usr/local/bin/goastgateway
# 実行ユーザーを指定
User=www-data
Group=www-data
# ログは systemd-journald に記録される
StandardOutput=journal
StandardError=journal
# 予期せぬ終了時に自動再起動する
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
```
この例ではCRM(ブラウザベース)を使っているので実行ユーザ/グループはwww-dataとしています。
# Asterisk側の設定
Asteriskからはchan_websocketで発信する必要があります。まず、接続先情報として /etc/asterisk/websocket_client.conf を設定します。以下の例をみてください。
```
[ws-ext-2001]
type = websocket_client
uri = ws://127.0.0.1:8765
protocols = media
connection_type = per_call_config
connection_timeout = 500
reconnect_interval = 500
reconnect_attempts = 5
tls_enabled = no

[ws-ext-2002]
type = websocket_client
uri = ws://127.0.0.1:8765
protocols = media
connection_type = per_call_config
connection_timeout = 500
reconnect_interval = 500
reconnect_attempts = 5
tls_enabled = no
```
[ws-ext-2001]のような記述はエンドポイントの識別子です。Asteriskの他のチャネル設定と同じですね。

他の項目は見ての通りなのですが、uriはGAGの「左側」つまりAsterisk用のアドレスとポートを指定します。なお、この部分は同一サーバ内で使うのが前提なのでSSLを使いません(ws://)。

エンドポイントはそれぞれの内線毎に書く必要があります。内線番号とエンドポイントを別に管理する抽象化を行いたい場合にはちょっとイヤではあるのですが、現状のGAGの仕様ではここに内線を指定する必要があります。

ws-ext- が識別子でGAGの設定で exten_search_pattern で指定したものになります。これに続く部分が「内線番号」として識別されます。

なお、websocket_client.confファイルを変更した場合には
```
*CLI> module reload res_websocket_client.so
```
を実行してください。

内線に対して発信するには以下のようにします。
```
exten => 2001,1,Dial(Websocket/ws-ext-2001/c(slin16)n,,r)
```
- c(slin16) : CODECの指定です。Signed Linear 16を使います。
- n : Dial時即Answerしません。chan_websocketは何も指定しないとAnswerしてしまうためこのオプションを付けます。
- r : DialのオプションでRingingインディケーションさせます。chan_websocketはAnswerするまでは「黙って」しまうので'r'を付けることでRingBackさせます。

これでGAGに対して発信することができ、GAG側で内線2001が接続されていれば通話することができます。

参考:
```
exten => 2001,1,Ringing()
exten => 2001,2,Dial(Websocket/ws-ext-2001/c(slin16)n,,r)
```
これをやると「おかしな」挙動になります。chan_websocketは'n'を指定しているにも関わらず勝手にAnswerします。現在のところchan_websocketは他のチャネルと同等の挙動をするようではないようです。

JSON形式の使用:

Asteriskの特定のバージョン以降(例えば22.8.0以降の予定)ではメッセージのフォーマットとしてJSON形式が使えます。JSON形式ではチャネル変数が引き渡せるため、より柔軟な設定が可能となります。以下の例を見てください。
```
exten => 2001,1,Set(_WS_EXTEN=2001)
exten => 2001,2,Dial(Websocket/ws-ext-endpoint/f(json)c(slin16)n,,r)
```
チャネル変数WS_EXTENを継承用に"_"を付けることでDialに渡せます。WS_EXTENはGAGの設定例でみたようにJSON形式の場合の内線判別用変数(設定で指定可能)です。つまりエンドポイントの設定は「ひとつだけ」あればよく、どの内線に着信させるかはチャネル変数で指定することができるため、より柔軟なDial()が可能となります。

ただしこの方法は以下のようなmixedでは使えない(全部同じ内線になってしまう)ので、JSONでチャネル変数を使うか、textでエンドポイント識別を使うかはAsteriskの構成に応じて選んでください。
```
exten => 2000,1,Dial(PJSIP/phone-2001&Websocket/ws-ext-endpoint/f(json)c(slin16)n&Websocket/ws-ext-endpoint/f(json)c(slin16)n,,r)
```
グループ着信機能をGAGに設けている理由はここにあります。単一のエンドポイントで複数の内線を同時に鳴動させたい場合には以下のようにします。
```
exten => 2001,1,Set(_WS_EXTEN=G01)
exten => 2001,2,Dial(Websocket/ws-ext-endpoint/f(json)c(slin16)n,,r)
```
GAGで設定済のグループ、G01に対してJSON形式で発信すると、GAGは登録されているグループ内の内線を鳴動させます。
# ブラウザフォン・デモ
デモ用のブラウザフォンを添付しています。もともと、これをやりたかったのでGAGを開発することになったのですが。

GAG経由でのWebsocket通話は単純なTCPのみの通話です。このため、ブラウザ側にSIPスタックなど、導入が煩雑になるようなコンポーネントを一切必要としません。このためCRM等に組み込む場合に容易になるようにしてあります。前述のようにこの「電話機」は着信専用です。発信する場合にはAMI経由等でコールバック方式を使ってください。

接続にはSSL(https://)を必要とします。https接続されればWebsocketの接続もwssになります。http+wssのようなmixedは許容されませんので注意してください。

なお、WebsocketのTCPのみによる通話なので"TCPによる通話"をよく理解した上で使ってください。お前はいったい何を言っているんだ？と思われるでしょう、そうでしょう。TCPで通話する際の制限や品質に十分注意して使ってくださいということです。

その反面、メリットもあります。Websocketしか使わないのでNAT越えが簡単ですし、ひとつしかポートを使わないのでFWの設定も簡単です。昨今のネット環境で高速な回線を使えるのならばTCP通話でもいけるでしょう。

基本的に設定類はindex.php内をみればわかるようになっています。

内線番号を「騙って」しまえばログインできてしまうじゃないか！と思われるかもしれませんが、そもそもCRM等でログインを行わせる想定なのでユーザ認証、ログイン処理、内線番号取得等は「別な」とこで行ってください。当たり前の話ですが秘密鍵の管理はちゃんとしましょう。
