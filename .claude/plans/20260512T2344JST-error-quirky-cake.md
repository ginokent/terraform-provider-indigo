# indigo_instance に stop_before_destroy オプションを追加する

## Context

`terraform destroy` (および ForceNew フィールド変更による replacement) で `indigo_instance` を消そうとした際、対象が RUNNING だと Indigo API が以下で拒否してくる:

```
status=400 message=Instance is in running status. Please stop the instance before destroying.; I10055
```

毎回手動で stop を挟むのは UX が悪い。ただし「常に自動停止」をデフォルトにすると、起動中の本番 VM が `terraform destroy` で意図せず止められる事故を招く。そこで **明示的な opt-in フラグ `stop_before_destroy`** を追加し、true のときだけ destroy 前に PowerStatus=STOPPED を確認してから destroy する。

合意済みの設計判断:
- **デフォルト false (opt-in)** — 安全側に倒す
- **PowerStatus=STOPPED を 5分待つ** — 既存の `waitForPowerStatus` / `powerConvergeTimeout` を流用
- 命名は `stop_before_destroy` (AWS の `force_destroy` に近い意味)

GCP の `allow_stopping_for_update` とは厳密には異なる (向こうは更新時。今回は destroy 時)。

## 編集ファイル

### `internal/provider/resource_instance.go`

**(1) Schema に `stop_before_destroy` を追加** (`resourceInstance()` 内、`ipv4` の直後):
```go
"stop_before_destroy": {
    Type:     schema.TypeBool,
    Optional: true,
    Default:  false,
},
```
コメントで「opt-in にする rationale (本番 VM 事故防止)」と「true 時の挙動 (stop → STOPPED 待ち → destroy)」を godoc に書く。

**(2) `resourceInstanceDelete` の `apiClient` と id 取得の後・`c.DeleteInstance` の直前に分岐を追加**:
```go
if d.Get("stop_before_destroy").(bool) {
    if err := ensureStoppedForDestroy(ctx, c, id); err != nil {
        return opDiag("indigo_instance", "delete", err)
    }
}
```

**(3) `ensureStoppedForDestroy` を新規実装** (`waitForPowerStatus` の前あたり):
- `c.GetInstanceByID(ctx, id)` で現状取得
- inst == nil → 既に消えている → return nil (out-of-band 削除)
- `normalizePowerStatus(inst.PowerStatus) == "STOPPED"` → return nil
- `normalizePowerStatus(inst.PowerStatus) == "RUNNING"` → `c.UpdateInstanceStatus(ctx, id, "stop")` 発行
  - `isIdempotentStatusUpdateError(err, "stop")` ならエラー無視 (read と stop の間に out-of-band stop されたレース)
  - その後 `waitForPowerStatus(ctx, c, id, "STOPPED", powerConvergeTimeout)` で STOPPED 確認
- **それ以外 (遷移中文字列。例: "OS installation In Progress")** → 明示的にエラーを返す: `fmt.Errorf("instance is in transient state %q; cannot stop_before_destroy. wait for lifecycle to converge and retry", inst.PowerStatus)`
  - 理由: READY (provisioning 中) の VM に stop を投げる挙動は Indigo 側で未定義。lifecycle 自動収束待ちは overengineering なので、明確なエラーでユーザに任せる。lifecycle=READY 経由で到達するのは中途失敗 create のリカバリといった稀ケース
- どのエラーも `fmt.Errorf("stop before destroy: %w", err)` で wrap し、destroy 経路で起きたことを明示

## 再利用する既存ユーティリティ

- `normalizePowerStatus(s string) string` (resource_instance.go:319 付近) — PowerStatus → "RUNNING"/"STOPPED" 正規化
- `waitForPowerStatus(ctx, c, id, want, timeout)` (resource_instance.go:290 付近) — STOPPED 待ち
- `isIdempotentStatusUpdateError(err, command)` (resource_instance.go:334 付近) — 既に stopped を吸収
- `powerConvergeTimeout` 定数 (= 5分) — 待機タイムアウト
- `c.GetInstanceByID` / `c.UpdateInstanceStatus` — 既存 client API

新規実装の追加コードは ~25 行程度。

## テスト

`internal/provider/resource_instance_test.go` に追加。既存テストはユニット形式 (httptest を直接使わず関数を直接叩く) なので、新規テストは **`ensureStoppedForDestroy` を httptest server 経由でカバー** する形にする (`client_test.go` の `TestCreateAndGetInstance_WithInconsistentPayloadShapes` パターンを踏襲)。

追加するテストケース:

1. **`TestEnsureStoppedForDestroy_Running`**: RUNNING で呼ぶと statusupdate に "stop" が送られ、その後の getinstancelist で STOPPED が返れば err=nil
2. **`TestEnsureStoppedForDestroy_AlreadyStopped`**: STOPPED 状態で呼ぶと statusupdate が叩かれず即 return
3. **`TestEnsureStoppedForDestroy_InstanceGone`**: GetInstanceByID が nil を返したら err=nil (out-of-band 削除許容)
4. **`TestEnsureStoppedForDestroy_AlreadyStoppedRace`**: stop コマンド送信時に I10017 "already stopped" が返っても、STOPPED を確認できれば err=nil。既存 `TestIsIdempotentStatusUpdateError` は関数単体テストなので、`ensureStoppedForDestroy` 経由のレース挙動の regression 防止としては別に必要
5. **`TestEnsureStoppedForDestroy_TransientState`**: PowerStatus が "OS installation In Progress" のとき、stop を投げずに「transient state」エラーを返すこと

Schema の追加自体はコンパイラレベルで保証される (`resourceInstance().Schema["stop_before_destroy"]` の存在チェックは省略)。

## 動作確認手順

1. `go test ./...` — 既存・新規テストすべてグリーン
2. `make install` でローカル mirror に入れ替え
3. `examples/ssh-key-vm/` で:
   - `stop_before_destroy = true` を main.tf に明記
   - `terraform apply` で RUNNING のインスタンスを作成
   - `terraform destroy` で自動 stop → destroy が通ることを確認 (TRACE ログで "stop before destroy" 系の挙動を観測)
4. 比較: `stop_before_destroy = false` (デフォルト) で RUNNING のまま destroy → 従来通り I10055 で fail することを確認 (regression がないこと)

## ドキュメント

- `SPEC.ja.md` の indigo_instance 仕様セクションに `stop_before_destroy` を追記。以下 3 点を含む:
  1. opt-in 設計の rationale (本番 VM の事故防止)
  2. **失敗時の振る舞い**: stop 待機 (powerConvergeTimeout = 5分) が timeout した場合 destroy は走らず error 終了。次回 `terraform destroy` で自動リトライ可能 (`isIdempotentStatusUpdateError` が「既に stopped」を吸収)
  3. **drift の発生条件**: destroy が中途失敗 (例えば stop は通ったが getinstancelist ポーリング中に context が切れた) すると、state 上 `instance_status` が `RUNNING` のまま残り、次回 plan で `RUNNING → STOPPED` の drift が出ることがある。これは正しい挙動 (Read が API 値を素直に書くため)
  4. **スコープ外**: lifecycle=READY (provisioning 中) のインスタンスは「transient state」エラーで拒否される。これは ForceNew 経由の中途失敗 create リカバリといった稀ケース。手動で lifecycle=OPEN への収束を待ってから retry してほしい
- `README.md` の例には載せない (上級者向けオプションでデフォルト動作には影響しない)
- 詳細設計は schema コメントと `ensureStoppedForDestroy` の godoc に記載

## Timeout 設計

Terraform SDK v2 の Delete 操作のデフォルトタイムアウトは 20 分。今回の最悪ケース内訳:
- `ensureStoppedForDestroy` の `waitForPowerStatus` で最大 5 分
- 既存の destroy 後ポーリングで最大 2 分
- 計 7 分 (+ API レート制限による x-quota 待機 < 1 分程度)

20 分のマージンに収まるため、`Timeouts.Delete` の明示設定はしない。レート制限が極端に逼迫する状況では 20 分を超えうるが、その場合は SDK のデフォルト timeout が先に切れるのが妥当な失敗モード。

## やらないこと

- update 時の auto stop (GCP `allow_stopping_for_update` 相当) — 今回の ForceNew フィールド構成では不要 (`instance_status` は Optional+Computed で stop/start を専門に扱っている)
- destroy 失敗時の自動 start 復旧 — Indigo の destroy 自体が冪等でない世界で start に戻すのは危険、スコープ外
- `force_destroy` 命名 — AWS の force_destroy は「子リソースごと消す」意味が強く、本件 (事前停止) とはニュアンスが違うため避ける
- 後方互換性の維持 (CLAUDE.md 方針)
