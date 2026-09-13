# readeckorator

Readeck bookmark classifier powered by an OpenAI-compatible LLM.

## What it does

- Fetches new bookmarks from your self-hosted [Readeck](https://readeck.org/) instance.
- Sends the title, URL, description, and full article body (markdown export) to an OpenAI-compatible LLM.
- Applies the labels the LLM returns (additive only — never removes existing labels).
- Optionally reconciles Readeck collections from configured label groups.
- Tracks which bookmarks it has already processed, so re-runs only classify new ones.

## Status

Phase 1 scaffold. CLI surface and config loader are wired but the
classification pipeline is not yet implemented. See `docs/` for the
roadmap.

## Quick start (when phase 12 lands)

```sh
# 1. Create a Readeck API token (Profile → API Tokens)
export READECK_API_TOKEN="..."

# 2. Point it at any OpenAI-compatible endpoint
export LLM_API_KEY="..."

# 3. Write a config
cp configs/readeckorator.example.yaml readeckorator.yaml
$EDITOR readeckorator.yaml

# 4. Run
./bin/readeckorator run            # one-shot pass
./bin/readeckorator serve          # daemon mode (poll every 5m by default)
```

## Build

```sh
go build -trimpath -o bin/readeckorator ./cmd/readeckorator
```

## Test

```sh
go test ./...
golangci-lint run
```

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
