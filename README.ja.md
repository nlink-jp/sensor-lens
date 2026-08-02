# sensor-lens

SwitchBot のセンサーから温度・湿度・CO2 を収集し、履歴をローカルに残して、
アプリを開かずに読み返すためのツール。

`sensor-lens` は CLI エンジンです。SwitchBot Open API v1.1 で認証し、
定期的にデバイスを読んで、すべての測定値をローカルの SQLite に保存します。
メニューバー GUI (`sensor-lens-gui`、別リポジトリ) は `--json` を叩く薄い
フロントエンドです。

**読み取り専用**です。デバイスへコマンドを送ることはないので、何かを
オン・オフしたり、開けたり、施錠したりすることは構造的にできません。

## 必要なもの

- **SwitchBot ハブ** (ハブミニ / ハブ2 / ハブ3)。温湿度計は Bluetooth で
  通信するため、クラウド API からはハブ経由でしか見えません。アプリで
  クラウドサービスを有効にしておく必要があります。
- アプリで発行するトークンとシークレット: プロフィール → 設定 →
  **アプリバージョン**を10回タップ → 開発者向けオプション → トークンを取得
  (アプリ v6.14 以降)。
- macOS または Linux。デーモンの自動起動は macOS (launchd) 用に組んであります。
  それ以外では `sensor-lens daemon` を自前のスーパーバイザで動かしてください。

## インストール

```bash
make build      # -> dist/sensor-lens
```

`config.example.toml` を参考に `config.toml` を作成します。置き場所:

- macOS: `~/Library/Application Support/sensor-lens/`
- その他: `~/.config/sensor-lens/`

```toml
[switchbot]
token  = "..."
secret = "..."
```

パーミッションは 0600 にしてください。`sensor-lens doctor` が確認します。

```bash
sensor-lens doctor        # 認証情報・API 予算・疎通を診断
sensor-lens devices       # アカウント上のデバイスを検出
sensor-lens now           # いまのセンサー値を読む
sensor-lens install       # ログイン時に収集を開始 (macOS)
```

## 何を収集するか

status のレスポンスは機種ごとに分岐せず、フィールド単位で読みます。数値
フィールドはすべて metric になり、名前付けだけがテーブル駆動です。新機種や
ファームウェア更新でフィールドが増えても、実装を変えずに収集されます。

| API フィールド | 保存名 | 対象デバイス |
|---|---|---|
| `temperature` | `temperature_c` | 全温湿度計、ハブ2/ハブ3、デイリーステーション |
| `humidity` | `humidity_pct` | 同上 |
| `CO2` | `co2_ppm` | **CO2センサー (Meter Pro CO2) のみ** |
| `battery` | `battery_pct` | 電池駆動の温湿度計 |
| `lightLevel` | `light_level` | ハブ2 (1–20)、ハブ3 (1–10) |
| その他の数値 | API のフィールド名のまま | それを返すデバイス |

自動で収集対象になるのは**温度または CO2 を返すデバイスだけ**です。この2つが
「デバイス自身ではなく部屋の状態」を表す測定値だからです。湿度だけでは足りません
— 加湿器も humidity フィールドを（自分の mode や childLock と並べて）返すため、
収集すると API 予算をノイズに使ってしまいます。除外されたデバイスも
`[polling] devices` に名前を書けば収集できます。

`sensor-lens devices --raw` で API が実際に返した生のレスポンスを確認でき、
`devices --reclassify` で収集対象を判定し直せます（デバイス1台につき1コール）。

## コマンド

| コマンド | 内容 |
|---|---|
| `devices [--json] [--raw] [--refresh] [--reclassify]` | デバイス一覧と収集対象かどうか |
| `now [--json] [--devices ids] [--stored]` | いまの値を読む (`--stored` は保存済みの最新値) |
| `daemon` | 常駐コレクターを実行 |
| `history --device D --metric M [--since --until --bucket 5m] [--json]` | 1 metric の時系列 |
| `report [--since --until] [--json]` | metric ごとの min / avg / max |
| `gaps [--since --until --factor N] [--json]` | 測定値が無い区間 |
| `import --file F [--device D] [--dry-run] [--all-columns] [--tz Z]` | アプリの CSV を取り込む |
| `export [--format csv\|json] [--since --until]` | 保存済みデータを書き出す |
| `status [--json]` | デーモン状態・DB パス・本日の API コール数 |
| `doctor` | 設定・認証情報・予算・疎通の診断 |
| `install` / `uninstall` | launchd LaunchAgent の登録 / 解除 |
| `prune [--keep-days N \| --device D] [--dry-run]` | 古い測定値、または特定デバイスの記録を削除 |
| `version` | バージョン表示 |

時刻オプションは `2026-08-01`、`2026-08-01 15:04`、相対指定 `-3h` / `-7d` を
受け付けます。

## 「収集する」と「表示する」は別

`[polling] devices` は**収集対象 (collect set)** です。ポーリングして DB に
入れるデバイスを指定します。空にすると、環境測定値を返すデバイスをすべて
収集します。電圧しか返さないプラグや Bot は対象外になります。

メニューバーに何を出すかはこれとは別の、もっと狭い選択で、GUI が持ちます。
CLI での対応物は `now --devices` です。**8台収集して2つだけ表示**、が普通の
使い方になります。

## 履歴のバックフィル

**SwitchBot API に履歴取得のエンドポイントはありません。** 現在値しか返さない
ため、デーモンが止まっていた区間は API からはどうやっても復元できません。

ただしデータ自体は存在します。機器内に数週間分、アプリ内に数年分。戻す経路は
アプリのエクスポートです。

```bash
sensor-lens gaps                     # どこがどれだけ欠けているか
# SwitchBot アプリ → 対象デバイス → データをエクスポート → 期間を選択 → CSV を共有
sensor-lens import --file "CO2センサー (Room 1)_data.csv" --dry-run
sensor-lens import --file "CO2センサー (Room 1)_data.csv"
```

取り込みは**冪等**です。測定値は (デバイス, metric, タイムスタンプ) で識別
されるため、重なる期間をエクスポートしても本当に足りない分だけが入ります。
同じファイルを何度取り込んでも壊れません。

エクスポートについて知っておくべきこと:

- アプリは約1分間隔で記録しており、既定の5分ポーリングよりずっと密です。
  取り込んだ区間だけ解像度が上がります。
- タイムスタンプにタイムゾーンが入っていません。実行マシンのローカル時刻として
  解釈します。別の地域で取ったエクスポートなら `--tz` を指定してください。
- アプリは露点・VPD・絶対湿度も計算して出力します。これらは**既定では
  取り込みません**。API 側が返さないため、取り込んだ区間にしか存在しない
  系列になってしまうからです。必要なら `--all-columns` を付けてください。

## API 予算を守る

アカウントの制限は**1日 10,000 コール**で、超過しても派手に失敗しません。
API が "Unauthorized" を返すようになるだけで、トークンが間違っている場合と
区別がつきません。

そのため sensor-lens は消費したコール数を DB に記録します。再起動しても
忘れません。

- 既定の5分間隔なら 1日あたり `デバイス数 × 288` コール。センサー6台で
  約1,700コールなので、予算内に十分収まります。
- `doctor` と `install` は `api.daily_budget` (既定 8000) を超える設定を
  **拒否**し、収まる間隔を提示します。
- デーモンは予算を超える前にその日のポーリングを止め、API に断られた場合は
  指数バックオフします。

## ストレージ

(デバイス, metric, タイムスタンプ) ごとに1行、実測で約65バイトです。
センサー5台を既定の間隔で回すと年間およそ 100 MB。`prune` と
`storage.retention_days` (既定 400) で上限を保てます。

DB と設定ファイルは所有者のみ (0600) にします。分単位の温度ログは、
いつ人が家にいたかの記録でもあるためです。

## 開発

```bash
make build      # -> dist/ (`go build` を直接使わない)
make test       # go test ./...
make vet        # darwin + linux + windows
```

cgo は使いません。SQLite は pure Go (`modernc.org/sqlite`) なので、Mac から
全プラットフォームへクロスコンパイルできます。構成と注意点は `AGENTS.md`、
設計の根拠は `docs/ja/sensor-lens-rfp.ja.md` を参照してください。

## ライセンス

MIT — `LICENSE` を参照。
