# 429 リトライを Indigo 独自ヘッダ駆動に置き換える

## Context

`indigo_instance` の Read で 429 が Terraform に到達してエラー終了する問題が発生。エラー文には "The provider retries automatically" と書かれているのに救えていない。

実際の 429 レスポンスを確認したところ、Indigo は **`Retry-After` を返さず**、代わりに以下の独自ヘッダを返す:

```
x-quota-reset:     1777654500000   (Unix epoch ms — クォータ復活時刻)
x-quota-allowed:   6                (ウィンドウ内の最大リクエスト数)
x-quota-available: 0                (残量)
```

現在の実装 (`internal/client/client.go`):
- `Retry-After` のみを参照 (`retryAfter()` L301-316)
- ヘッダ無し時は `(attempt+1) * 1秒` の線形バックオフ → 5 attempt 計 **10秒** で諦め
- `minInterval = 600ms` 静的スロットル ≈ **1.67 req/s**
- 観測された制約は **6 req/window (おそらく1分) ≈ 0.1 req/s**

つまり静的閾値は **17倍速すぎ**、リトライ予算は **クォータウィンドウより短い**。リトライは「形式上動いているが構造的に救えない」状態。

ゴール: ヘッダ駆動の reactive リトライ + adaptive 事前スロットルで、`-parallelism=1` でなくても普通に動くようにする。

## 方針

1. **429 時は `x-quota-reset` までスリープ** (`Retry-After` をまず見て、無ければ `x-quota-reset` を見る順)
2. **すべてのレスポンスから `x-quota-available` / `x-quota-reset` を観測**し、`x-quota-available <= 1` なら次リクエスト発射前に reset まで待機 (adaptive 事前スロットル)
3. **静的 `minInterval = 600ms` は撤廃**。adaptive 状態 (`quotaResetAt`, `quotaAvailable`) を `*Client` に持たせ、`waitRateLimit` をそれ参照に書き換える
4. **ヒント文を現実に合わせる** (Retry-After ではなく x-quota-reset 駆動になる旨は出さない — これは内部実装。ユーザ向けには「クォータ枯渇、自動的に reset まで待機している。継続発生時は parallelism を下げる」)
5. maxAttempts は **5 のまま据え置き**。各 attempt が reset まで正確に待つので、5回あれば数分の輻輳をカバーできる

## 編集ファイル

### `internal/client/client.go` (主要)

**`Client` 構造体 (L22-31):**
- `minInterval time.Duration` を削除
- 代わりに以下を追加 (mu 配下で保護):
  ```go
  quotaResetAt   time.Time  // 次に quota が復活する時刻 (ヘッダから観測)
  quotaAvailable int         // 直近観測した残量 (-1 = 未観測)
  ```

**`New()` (L40-57):** `minInterval` 初期化を削除、`quotaAvailable: -1` で初期化

**`waitRateLimit()` (L199-224):** 全面書き換え
- `quotaAvailable <= 1` かつ `quotaResetAt` が未来 → reset まで sleep
- それ以外は即 return (静的 600ms スロットルは撤廃)
- ctx 対応・mu 排他は維持

**`do()` の 429 ブランチ (L162-175):**
- `Retry-After` を最優先で参照、無ければ `x-quota-reset` ヘッダを参照
- どちらも無いとき初めて静的 fallback (`(attempt+1) * 1秒`) に落ちる
- 加えて、**429 を受けた時点で `quotaResetAt` / `quotaAvailable=0` を mu 配下で記録** (次のリクエストが事前待機できるように)

**`do()` の成功・他エラー応答処理 (L153-191):**
- レスポンスヘッダ取得直後に `recordQuota(resp.Header)` を呼び `quotaResetAt` / `quotaAvailable` を mu 配下で更新

**新規 helper:**
- `parseQuotaReset(h string) time.Time` — Unix epoch ms → time.Time、不正値は zero time
- `parseQuotaAvailable(h string) int` — int 化、不正値は -1
- `(c *Client) recordQuota(h http.Header)` — 上 2 つを呼んで quota 状態を更新
- 既存の `retryAfter()` はそのまま (Retry-After 用)

**`errorHint()` (L288-299) の 429 メッセージ更新:**
- 現状: "The provider retries automatically, but consider reducing parallelism..."
- 新: "API quota exhausted. The provider waits until the quota window resets (x-quota-reset) before retrying. If this persists, reduce concurrency (e.g. terraform apply -parallelism=1) or contact Indigo support to raise the quota."

### `internal/client/client_test.go`

新規テストを追加 (既存テスト粒度には合わせず、必要な範囲で書く):

1. **`TestDo_429_UsesQuotaResetHeader`**: httptest server が 1回目 429 + `x-quota-reset` (近未来) を返し、2回目 200 を返す。client は reset 時刻まで待ってから 2回目を発射することを確認 (経過時間で検証、jitter ±100ms で許容)
2. **`TestDo_429_PrefersRetryAfterOverQuotaReset`**: Retry-After=1s と x-quota-reset=遠未来 が同時に来たら Retry-After 優先
3. **`TestWaitRateLimit_AdaptivePrefetch`**: 1回目レスポンスで `x-quota-available: 0` + `x-quota-reset` を観測したら、2回目リクエストは reset まで待機
4. **`TestWaitRateLimit_AvailablePositive_NoWait`**: `x-quota-available: 5` なら待機なしですぐ次へ
5. **`TestParseQuotaReset` / `TestParseQuotaAvailable`**: 不正値での挙動

## 再利用する既存ユーティリティ

- `sleepWithContext()` (L318-330) — そのまま流用
- `retryAfter()` (L301-316) — `Retry-After` パース、そのまま流用
- `errorHint()` (L288-299) — 文面のみ更新、構造は維持

## 動作確認手順

1. `go test ./internal/client/...` — 既存・新規テスト全通過
2. `go test ./...` — provider 側まで通過
3. `make install` でローカル mirror に入れ替え
4. 手元の `examples/ssh-key-vm/` で `terraform plan` (Read 経路) を `-parallelism=4` で繰り返し実行し、429 を踏ませる:
   - 期待: ログ (`tflog.Debug`) に "waiting for quota reset" 等の adaptive 待機が現れ、最終的に成功
   - エラーで止まらないこと
5. 単発 quota 枯渇シナリオを `curl` で誘発した直後に `terraform plan` を実行し、自動的に 30〜60 秒待って通ることを確認

## やらないこと

- maxAttempts の変更 (現行 5 で adaptive と組み合わせれば十分)
- env (`WEBARENA_INDIGO_MIN_INTERVAL` 等) による外部調整可能化 (ヘッダ駆動なら不要)
- `Retry-After` 実装の削除 (Indigo が将来返すかもしれず、汎用 fallback として温存)
- 後方互換性の維持 (CLAUDE.md 方針: 後方互換性 / fallback は不要)
