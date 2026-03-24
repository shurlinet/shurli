All Shurli user-level data now lives under `~/.shurli/` instead of being split across `~/.config/shurli/` (config) and `~/.shurli/` (backups, ZKP cache). This matches the single-dotdir convention used by Docker, IPFS, and similar tools.

### What changed

- **macOS and Linux user installs** default to `~/.shurli/` for config, identity, authorized keys, plugins, backups, and ZKP cache.
- **Root/system installs** (`/etc/shurli/`) are unchanged.
- **Existing installs** keep working. The daemon searches `~/.shurli/` first, then `~/.config/shurli/` as a fallback. Running `shurli init` on a machine with the old path offers to migrate automatically.
- **Install script on macOS** no longer requires sudo for config creation. Backup restore targets `~/.shurli/` on macOS. Uninstall preserves backups when removing config.

### Install

```
curl -sSL https://raw.githubusercontent.com/shurlinet/shurli/dev/tools/install.sh | SHURLI_DEV=1 sh
```

### Release notes automation

Release notes are now auto-generated from conventional commits, grouped into Features, Bug Fixes, and Other Changes. Human-written summaries (like this one) are prepended when `RELEASE_NOTES.md` exists in the repo at tag time.
