# Minecraft Bedrock Bot (Go)

既存の統合版（Bedrock Edition）サーバーへプレイヤーとして接続するBotサービスです。Go + [Gophertunnel](https://github.com/Sandertv/gophertunnel) + HTTP APIを使用します。Minecraftサーバー本体は別途用意してください。

## Dockerで起動

1. `.env.example` を `.env` にコピーします。
2. `MC_SERVER_ADDRESS` に接続先の `ホスト:ポート` を設定します（通常UDP 19132）。`API_TOKEN` は24文字以上のランダムな値に変更します。
3. 起動します。

```sh
docker compose up -d --build
docker compose logs -f bot
```

`MC_AUTH_MODE=microsoft` の場合、初回はログに表示されるURLを開いてコードを入力し、Bot用Microsoftアカウントで認証してください。サーバーの許可リストも設定してください。表示名は認証アカウントのゲーマータグになります。認証トークンは `bot-data` ボリュームに保存され、再起動後も利用します。ログインコードとボリュームは他人に共有しないでください。

Docker DesktopからホストPCへ接続する場合は `host.docker.internal:19132` を指定します。Linuxでは接続先のLAN IPやDNS名を指定してください。同じComposeネットワーク内のサーバーならサービス名を指定できます。Botは外向きにUDP接続するため、Bot側の19132ポート公開は不要です。

自分で管理する認証なしサーバーには `MC_AUTH_MODE=offline` で接続できます。この場合の名前は `MC_BOT_NAME` です。通常の認証必須サーバーではMicrosoft認証を使用してください。

## 機能

- 接続・スポーン・切断時の自動再接続（`RECONNECT_DELAY`）。
- ゲーム内の `!ping` に `pong`、`!help` にヘルプを返します。応答は全体で2秒に1回までです。
- HTTP APIで接続状態確認と発言。
- SIGTERMで終了、コンテナのヘルスチェック、認証情報の永続化。

移動・採掘・戦闘などの自律行動は未実装です。Botはプレイヤー枠を1つ使用します。サーバー側のAFKキックやチャット権限にも従います。

## API

既定ではホストの `127.0.0.1:8080` からアクセスします。`TOKEN` は `.env` の `API_TOKEN` に置き換えてください。

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl -H "Authorization: Bearer TOKEN" http://localhost:8080/api/status
curl -X POST http://localhost:8080/api/chat -H "Authorization: Bearer TOKEN" -H "Content-Type: application/json" -d '{"message":"こんにちは！"}'
```

`/healthz` はプロセスの稼働確認です。`/readyz` はBotの接続時に200、未接続時に503です。Dockerのヘルスチェックは認証待ちや接続障害でプロセスを異常扱いにしないよう `/healthz` を使用します。発言APIは成功時204、認証失敗401、入力不正400、未接続や送信失敗503です。メッセージは改行なし・最大512バイトです。管理APIを外部へ公開する場合はTLSとアクセス制御を備えたリバースプロキシを使用してください。

## 設定

| 環境変数 | 既定値・用途 |
|---|---|
| `MC_SERVER_ADDRESS` | 必須、接続先 `host:port` |
| `MC_AUTH_MODE` | `microsoft` または `offline` |
| `MC_BOT_NAME` | `GoBot`、offline時の名前 |
| `MC_TOKEN_FILE` | `/data/token.json` |
| `HTTP_ADDRESS` | `:8080`、コンテナではこの値を推奨 |
| `API_TOKEN` | 必須、24文字以上のランダムな秘密値 |
| `RECONNECT_DELAY` | `10s`、最低 `1s` |

## 開発・停止

Go 1.25以上で実行できます。ローカル実行では環境変数をシェルに設定してください（`.env` の自動読込はComposeのみ）。`MC_TOKEN_FILE` は書き込み可能なパスに設定します。

```sh
go test ./...
go vet ./...
go run ./cmd/bot
docker compose down
```

認証をやり直す場合は停止後にボリューム内の `/data/token.json` を削除します。`docker compose down -v` でも削除できますが、保存済み認証情報をすべて失います。

Gophertunnelは対応するBedrockプロトコルが限定されます。サーバーとライブラリの対応バージョンを合わせてください。本プロジェクトでは `v1.62.0` に固定しており、更新時は接続の再検証が必要です。
