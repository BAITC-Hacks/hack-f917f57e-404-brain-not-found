# Career Quest

Career Quest is an employee development platform built with Go and server-rendered HTML. It helps employees identify skill gaps, choose relevant learning activities, and track progress toward the next career grade. HR users can manage accounts, import datasets, and review team participation.

## Features

- Individual employee and HR accounts with role-based access.
- Skill profiles, next-grade requirements, and progress tracking.
- Up to three ordered development recommendations with supporting evidence.
- Optional AI-assisted recommendations with a clearly labeled rules fallback.
- Activity completion with recorded before-and-after skill changes.
- HR summaries for skill gaps, assessment coverage, and participation.
- Validated JSON and ZIP imports with preview for native ZIP datasets.
- Server-side sessions, CSRF protection, and atomic local persistence.

## Technology

| Component | Implementation |
| --- | --- |
| Backend | Go 1.27+, standard library |
| Interface | Server-rendered HTML templates and CSS |
| Storage | Local JSON files |
| Optional AI | OpenAI Responses API with validated structured selections |
| Dependencies | No external Go packages or frontend build required |

## Quick Start

Extract the complete project archive. Open a terminal in the directory containing `go.mod`, then run:

```powershell
go run ./cmd/career-quest
```

Open [http://localhost:567](http://localhost:567).

To use another port:

```powershell
go run ./cmd/career-quest --port 8080
```

The server binds to `127.0.0.1`. Run one instance per data directory. Press `Ctrl+C` to stop the server gracefully.

## First-Time Setup

1. Start the application. A fresh installation creates an empty workspace and an HR account.
2. Read the initial credentials locally from `data/private/initial-credentials.txt`.
3. Sign in as HR and import your dataset.
4. Create employee accounts for the imported profiles.
5. Sign in as an employee to review skills, generate recommendations, and record completed activities.

No sample employees or activities are bundled. Existing application data and accounts are loaded on subsequent starts. Keep credential delivery files private and remove them after securely distributing the credentials.

An operator can create or reset an account while the server is stopped:

```text
go run ./cmd/career-quest provision <username> <employee-id|hr>
```

Credentials are written to a private `data/private/delivery-*.txt` file. Restart the server after command-line account changes. Put configuration options before `provision` when using a custom data directory.

## Configuration

Command-line flags override environment variables. Relative paths resolve from the working directory.

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--port` | `PORT` | `567` |
| `--data-dir` | `CAREER_DATA_DIR` | `data` |
| `--secure-cookies` | `CAREER_SECURE_COOKIES=1` | Disabled |
| `--shutdown-timeout` | — | `15s` |
| `--ai-model` | `OPENAI_MODEL` | `gpt-4.1-mini` |
| `--ai-timeout` | — | `20s` |

These defaults describe the bundled application configuration.

```powershell
go run ./cmd/career-quest --data-dir "C:\CareerQuest\data" --port 8080
go run ./cmd/career-quest --help
```

Enable secure cookies when serving through an HTTPS reverse proxy. Leave them disabled for direct local HTTP access.

## AI-Assisted Recommendations

The application includes an optional OpenAI provider. It reads `OPENAI_API_KEY` from the process environment; `.env` files are not loaded automatically. The application can run without an API key using its rules engine.

The provider sends relevant skills, role and grade information, target requirements, and a bounded shortlist of eligible activities with history evidence. Employee names, employee and manager IDs, account records, and unrelated profiles are excluded. Activity titles and descriptions are included. Requests set `store: false`.

The model proposes ordered activities and evidence references. The Go backend validates eligibility, evidence, comparisons, and cumulative skill gains before saving recommendations. The model cannot directly modify employee skills.

Successful results are labeled **AI-ASSISTED**. Missing configuration uses the rules engine; timeouts, refusals, API errors, and invalid output produce a labeled **rules fallback**. Completion or import invalidates affected recommendations.

The default total AI budget is 20 seconds, including 16 seconds for generation. The implementation limits model requests to two concurrent slots and does not retry automatically. Live AI behavior requires valid credentials and model access; the presence of integration code alone does not establish successful live inference.

## Data and Imports

Application state is stored in `data/state.json`. Account records and credential delivery files are stored in `data/private/`. These paths are excluded from version control. Back up runtime data before replacing a dataset.

### JSON

A complete dataset contains `employees`, `requirements`, `activities`, and `history` arrays. Employee, activity, and requirement lists must be nonempty. Assessment levels are 0–10; target levels are 1–10. Missing assessments remain unknown.

Limits: 2 MB, 1,000 employees, 1,000 activities, and 20,000 history records. Field definitions are in `internal/careerquest/domain.go`.

### Native ZIP

Supported packages contain:

- `employees.json`
- `events.json`
- `skills.json`
- `activity_history.csv`

Upload a complete dataset to replace current data, or append employee profiles and history to an existing official catalogue. ZIP uploads are limited to 20 MB. Review the import preview before confirming.

Replacement clears recommendations and invalidates employee identity bindings; HR must assign accounts to imported profiles. Append imports preserve unchanged identity bindings. Previews expire after ten minutes and require regeneration if source data changes.

## Build and Static Analysis

Run from the project root:

```powershell
go build -o bin/career-quest.exe ./cmd/career-quest
go vet ./...
```

Run the executable from the project directory to use its `data/` folder, or specify `--data-dir`. Templates and styles are embedded in the binary, so no separate web directory is needed at runtime.

The archive does not bundle automated test files. The commands above build the application and perform static analysis; they do not establish end-to-end or live AI verification.

## Project Structure

```text
cmd/
  career-quest/
    main.go                 Command entry point and flags
internal/
  careerquest/
    config.go               Runtime configuration
    runtime.go              Startup and graceful shutdown
    server.go               HTTP workflows
    auth.go                 Accounts and sessions
    domain.go               Domain types and validation
    reasoning.go            Recommendation scoring and evidence
    provider.go             Provider contracts and validation
    openai.go               OpenAI transport
    progress.go             Skill progress calculations
    engagement.go           Participation indicators
    summary.go              Indexed summaries
    import_official.go      Native dataset adapter
    official_http.go        Import preview and commit
    store.go                Atomic local persistence
    assets.go               Embedded web assets
    web/
      templates/            Page layouts and reusable sections
      static/               Public styles and assets
data/                       Local runtime data
README.md
go.mod
```

## Scope and Limitations

- Designed for local operation with one process per data directory.
- Uses JSON persistence rather than a separate database server.
- Activity completion is self-reported.
- Meeting skill targets does not automatically promote an employee.
- AI recommendations depend on external API availability and configuration.

Use `gofmt` for Go source and keep runtime data, credentials, and generated binaries out of version control.
