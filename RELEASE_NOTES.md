Per-peer bandwidth budgets let you set different transfer limits for each authorized peer instead of a single global cap.

### What's new

- **Per-peer bandwidth budget** - set `bandwidth_budget` as a peer attribute (`unlimited`, `500MB`, `1GB`, etc.) to override the global default for specific peers.
- **Human-readable config** - `bandwidth_budget` in config.yaml now accepts `"500MB"`, `"1GB"`, `"unlimited"` instead of raw byte counts.
- **LAN peers exempt** - local network transfers skip bandwidth throttling entirely.
- **New CLI command** - `shurli auth set-attr <peer> <key> <value>` for setting peer attributes locally (previously relay-only).

### Usage

```bash
# Set per-peer budget
shurli auth set-attr 12D3KooW... bandwidth_budget 1GB

# Set unlimited for a trusted peer
shurli auth set-attr 12D3KooW... bandwidth_budget unlimited

# Config file (human-readable)
# plugins:
#   filetransfer:
#     bandwidth_budget: "500MB"
```

### Priority chain

LAN peer (always exempt) > per-peer attribute > config file > default (100MB/hr)
