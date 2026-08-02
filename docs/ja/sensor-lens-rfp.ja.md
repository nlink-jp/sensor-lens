# RFP: sensor-lens

> 作成: 2026-08-02
> ステータス: 承認済み、Phase 1 実装完了

## 1. 問題定義

部屋の温度・湿度・CO2 濃度を、SwitchBot アプリを開かずに一目で把握したい。
併せて履歴を残し、「昨日より悪いのか」に答えられるようにしたい。SwitchBot
アプリは現在値の表示は良いがスマホアプリであり、メニューバー常駐ツールは
見当たらない。

想定利用者は開発者本人（個人利用）。測定値はローカルに保存し、SwitchBot 自身の
API への認証済みリクエスト以外、外部へ何も送らない。

## 2. 機能仕様

### コマンド

Go CLI (`sensor-lens`) が収集・保存・集計を担い、Swift/SwiftUI のメニューバー
GUI は `--json` を叩く薄いフロントエンドとする。`active-lens` /
`active-lens-gui` と同じ二層構成。

| コマンド | 役割 |
|---|---|
| `sensor-lens daemon` | 常駐ポーリング。SQLite に追記 |
| `sensor-lens now [--json] [--devices ids]` | いまのセンサー値を読む |
| `sensor-lens devices [--json] [--raw] [--refresh]` | デバイス一覧と収集対象 |
| `sensor-lens history --device D --metric M [flags]` | 1 metric の時系列（GUI のグラフ元） |
| `sensor-lens report [--since --until]` | metric ごとの min / avg / max |
| `sensor-lens gaps [--since --until]` | 測定値が無い区間 |
| `sensor-lens import --file F [--dry-run]` | アプリの CSV を取り込む |
| `sensor-lens export --format csv\|json` | 保存済みデータの書き出し |
| `sensor-lens install` / `uninstall` | launchd LaunchAgent の登録/解除 |
| `sensor-lens status [--json]` | デーモン状態・DB パス・本日のコール数 |
| `sensor-lens doctor` | 設定・認証情報・予算・疎通の診断 |
| `sensor-lens prune --keep-days N` | 古い測定値の削除 |

### データモデル

測定値は long format で保存する。1行 = `(device_id, metric, ts)`。
ここから2つの帰結が出るが、両方が狙いである。

1. コードが知らない機種でも収集できる。抽出は「形」に基づき、status の
   レスポンス中の数値・真偽値スカラーはすべて metric になる。テーブル駆動なのは
   名前付けだけ。
2. バックフィルが冪等になる。主キーが測定値の同一性そのものなので、
   `INSERT OR IGNORE` は重なるエクスポートをマージして足りない分だけを入れる。

### 収集対象と表示対象

3層に分け、所有者を変える。

| 層 | 中身 | 所有者 |
|---|---|---|
| collect set | ポーリングして保存するデバイス | CLI の `config.toml` `[polling] devices`（daemon が必要とする） |
| ポップオーバー | 収集中の全デバイスをカード表示 | 固定（設定なし） |
| menubar set | バーに出す「デバイス × metric」の順序付きチップ | GUI の `UserDefaults`（CLI は読まない） |

menubar set は collect set の部分集合で、**デバイス × metric** 単位で選ぶ。
同じセンサーから CO2 だけをバーに出せる。8台収集して2つ表示、が想定構成。
CLI 側の対応物は `now --devices`。

### 設定

OS の config ディレクトリの `config.toml`、モード 0600（認証情報を持つため）。

- `[switchbot] token` / `secret` — 対話利用時は `SWITCHBOT_TOKEN` /
  `SWITCHBOT_SECRET` で上書き可
- `[polling] interval_seconds`（既定 300）、`devices`（collect set）
- `[api] daily_budget`（既定 8000）、`timeout_seconds`
- `[storage] db_path`、`retention_days`（既定 400）

### 外部依存

HTTPS 経由の SwitchBot Open API v1.1 と、アカウント上の SwitchBot ハブ。
それ以外の外部通信はしない。

## 3. 設計判断

- **CLI = Go、cgo なし。** SQLite は pure Go (`modernc.org/sqlite`) なので
  リリース全プラットフォームが Mac からクロスコンパイルできる。launchd の
  スケジューリングだけが OS 依存で、`_darwin.go` / `_other.go` に隔離する。
- **GUI = Swift/SwiftUI。** `active-lens-gui` と同じ署名・notarize パイプライン。
- **`StartInterval` ではなく常駐デーモン。** ポーラーは per-tick プロセスなら
  毎回読み直して再導出しなければならない状態（本日の消費コール数と現在の
  バックオフ）を保持している。
- **読み取り専用。** `POST /commands` を意図的に実装しない。鍵・カーテン・
  プラグを操作できないことを、ポリシーではなく構造で保証する。
- **認証情報は 0600 の設定ファイルにのみ置く。** LaunchAgent の plist は慣例上
  誰でも読めるため、`EnvironmentVariables` にトークンを置くのはファイルより
  明確に悪い。
- **テスタビリティ。** `metrics` / `aggregate` / `importer` / `switchbot/sign` と
  CLI の整形は純関数。API クライアントは `Doer`、ポーラーは clock を注入する
  ので、常駐ループ・予算・バックオフがネットワークも待ち時間もなしに検証できる。

### 明示的な scope 外

1. デバイスへのコマンド送信（恒久的に対象外）
2. SwitchBot Webhook の受信 — 公開 HTTPS エンドポイントが必要で、ローカル常駐
   ツールにはない。（将来案: 既存の `webhook-relay` を受け口にする。ただし
   webhook の `deviceType` は status とは別系統の文字列である点に注意）
3. ハブを介さない BLE 直接読み取り
4. クラウド同期・複数マシンの履歴マージ（DB を同期フォルダに置く単一 writer 運用まで）
5. SwitchBot 以外のベンダー — 名前は将来拡張可能だが v0.1.0 では扱わない

## 4. 開発計画

### Phase 0: プローブ

ドキュメントに書かれていないことを実機と実データで確定する。CSV 側は完了
（5デバイス分のエクスポート）。実 API のプローブはトークン発行待ち。

### Phase 1: CLI エンジン

単体でレビュー可能。署名付きクライアント、形ベースの抽出、SQLite ストア、
予算管理付き常駐ポーラー、CSV インポーター、欠測検出、上記コマンド群。
`make test` が緑で、実データでパイプラインが端から端まで検証できたら完了。

### Phase 2: GUI

選んだデバイス×metric のチップをメニューバーに表示。ポップオーバーは収集中の
全デバイス（現在値・1時間前比のトレンド・電池・stale バッジ）。Swift Charts の
分析ウィンドウ。CO2 しきい値アラート（1000 / 1500 ppm）でバーの色を変え、
任意で通知。設定は collect set / menubar set / 間隔 / 認証情報。履歴グラフには
欠測をハッチングし、export→import の導線を出す。

### Phase 3: リリース

ドキュメント、署名、notarize、submodule 登録、org profile、`check-org.sh`。

## 5. 必要な API スコープ / 権限

SwitchBot のオープントークンとシークレット。アプリで発行する（プロフィール →
設定 → アプリバージョンを10回タップ → 開発者向けオプション）。この API には
スコープの仕組みが無く、トークンはアカウント上の全デバイスの**操作**を含む
フルアクセスを与える。だからこそ本ツールはコマンド送信を実装しない。
認証情報が用途に対して過剰に強力である以上、抑制はクライアント側に置くしかない。

OS の権限は不要。macOS の自動起動はユーザーごとの LaunchAgent。

## 6. シリーズ配置

util-series。

`active-lens` / `claude-usage-lens` と同じ「Go CLI エンジン + Swift GUI、
Developer ID 署名 + notarize」の運用に乗る、ローカルの収集・集計・可視化
ユーティリティであり、`-lens` ファミリーの兄弟。

## 7. 外部プラットフォーム制約

- **1アカウント1日あたり 10,000 API コール。** 超過しても派手に失敗しない。
  API が "Unauthorized"（不正なトークンと同じ HTTP 401）を返すようになるだけ。
  そのため本日の消費数を DB に記録し（再起動で忘れてはならない）、既定の予算を
  制限値より低く置き、`doctor` / `install` は予算超過の設定を拒否する。
- **履歴エンドポイントが存在しない。** エンドポイントは9個（devices / status /
  commands / scenes×2 / webhook×4）で、status は現在値のみを返す。履歴は
  アプリの CSV エクスポート経由でしか復元できない。
- **ハブが必須。** 温湿度計は Bluetooth で通信し、クラウド API からは
  クラウドサービスを有効にしたハブ経由でしか見えない。
- **deviceType は安定したインターフェースではない。** 大半の機種で status の
  例が文書化されておらず、日本語の製品名と API の型文字列も一致しない
  （デイリーステーション = `Home Climate Panel`）。webhook の型文字列は別系統。
  ゆえに `deviceType` で分岐しない。
- **`Meter` の battery だけ4段階に量子化されている**（他機種は実際の割合）。
  機器の仕様であり、電池グラフでは階段状に見える。
- **エクスポートのタイムスタンプにタイムゾーンが無い**ため、取り込みは
  ローカル時刻を仮定する。
- SQLite を同期フォルダに置く場合は単一 writer を前提とする。

---

## 議論ログ

- 出発点は「温度・湿度・CO2 をメニューバーに、定期収集で」。実装前に認証方式・
  エンドポイント・1日あたりの上限を調査で確定させた。
- 収集対象と表示対象を別々に選べるようにという要望（多く集めて少なく出す）が
  あり、上記の3層モデルと、表示選択を「デバイス × metric」粒度にする設計に
  つながった。
- 併せて「取得可能なら未収集の履歴を埋めたい」という要望があった。調査の結果、
  API では履歴を一切取得できないことが判明。アプリの CSV エクスポートなら
  可能なため、`import` を冪等マージとして設計し、何をエクスポートすべきかを
  示す `gaps` を追加した。これは計画承認前に報告した（約束して黙って落とすこと
  はしない）。
- 公式 API ドキュメントの clone により残りの未確定事項が解決した
  （`CO2` フィールド名、`MeterPro(CO2)`、温度は常に摂氏、`Meter` の量子化
  battery、webhook の別系統型文字列）。
- 続いて5デバイス分の実エクスポートで CSV 形式を確定し、取り込み経路全体を
  それで検証した。15,519 件をマージし、再取り込みの挿入は 0 件。
