# CLAUDE.md

WebARENA Indigo (KVM VPS) 向け Terraform Provider。`terraform-plugin-sdk/v2` ベース。Go 1.24。

## まず読むべきもの

- **設計大方針 / 概要設計**: [`SPEC.ja.md`](SPEC.ja.md)
- **使い方 / 環境構築 (`~/.terraformrc` 設定など)**: [`README.md`](README.md)
- **詳細設計 / なぜそうなっているか**: 各 `.go` ファイルの godoc / インラインコメント

CLAUDE.md は Claude 向けの作業指針のみを保持し、設計内容は重複させない。

## 開発フロー

- `go test ./...` (全体) / `make test-client` (client のみ)
- `make install` でローカルにインストール (`~/.terraformrc` の `filesystem_mirror` 設定が前提、詳細は README)
- examples: `examples/ssh-key-vm/` (`terraform.tfvars.example` をコピーして使う)

## 作業上の注意

- 後方互換性 / fallback は不要。意味的に正しい実装を優先する
- 既存テストの粒度に合わせず適切にテストを書く
- ドキュメントは日本語。設計大方針は `SPEC.ja.md`、詳細設計と rationale はコード内の godoc / インラインコメントに置く (CLAUDE.md / README.md には書かない)
- 防御的実装 (shape 揺れ吸収・typo フォールバック・冪等エラー扱いなど) を簡略化する前に、対象関数の godoc を読み rationale を確認すること
