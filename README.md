
# <img src="assets/icon.png" alt="drawing" width="45"/>DayDan 



**Simplify Tasks & Chores, Together!**

DayDan is an open-source, user-friendly app designed to help you organize tasks and chores effectively. featuring customizable options to help you and others stay organized

![Screenshot](assets/screenshot.png)


![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/donetick/donetick/go-release.yml)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/donetick/donetick)
![Docker Pulls](https://img.shields.io/docker/pulls/donetick/donetick)


[![Discord](https://img.shields.io/discord/1272383484509421639)](https://discord.gg/6hSH6F33q7)
[![Reddit](https://img.shields.io/reddit/subreddit-subscribers/donetick)](https://www.reddit.com/r/donetick)

---

## Features

### Task & Chore Management
**Collaborative**: Create and manage tasks either solo or with family and friends. You can create a group and share or assign some of the tasks or chores with others.

**Natural Language Task Creation**: Describe what you need to do in plain English. DayDan automatically extracts dates, times, and recurrence patterns from phrases like “Change water filter every 6 months” or “Take the trash out every Monday and Tuesday at 6:15 pm.”

**Task Advanced Scheduling**: 
- Supports flexible scheduling: daily, weekly, monthly, yearly, specific months, specific days of the week, or even adaptive scheduling — where DayDan learns from historical completions to suggest due dates automatically.
- Due Date vs Completion Date Based Recurrence: Choose whether recurring tasks should be scheduled from the previous due date (ideal for a consistent cadence) or from the actual completion date (useful when tasks are often delayed).
- Assignee Rotation: Automatically rotate task assignments based on who has completed the fewest tasks, randomly, or in turns(round-robin) order.
- Time Tracking & Session Insights: Track how much time you spend on a task whether in a single session or across multiple.
  
**Subtasks with Smart Reset**: Break tasks into smaller steps with subtasks, each trackable on its own. For recurring tasks, subtasks automatically reset when the main task is completed. subtasks can be nested as well!

**Organize with Priorities and Labels**: Organize everything using custom labels and priorities. Labels can be shared across your group, making it easy to filter and sort tasks by category. Priorities help you stay focused  DayDan supports five levels: P1, P2, P3, P4, and No Priority.

**Add Photos**: Attach photos directly to tasks. Supports local storage (WIP) or cloud providers including AWS S3, Cloudflare R2, MinIO, and other S3-compatible services.

**Things**: A unique feature in DayDan. “Things” let you track data that isn’t a task. A Thing can be a number, boolean (true/false), or plain text. You can also mark tasks as done automatically when a Thing changes to a certain value.

**NFC Tag Support**: Create physical triggers by writing NFC tags that instantly mark tasks as complete when scanned.

### Gamification & Progress
**Points System**: Built-in points system that rewards task completion and tracks your progress over time.

**Completion Restrictions** : You can restrict task completion until a certain time, for example, make a task completable only within the last X hours before its due date. This helps prevent marking tasks as "done" too early.

**Comprehensive Analytics**: See task breakdowns by label, completion status, and other helpful graphs.

### Security & Authentication
**Multi-Factor Authentication**: Supports TOTP-based MFA.

**Multiple Sign-In Options**: Choose from local accounts or any OAuth2 provider that supports OIDC, like Keycloak, Authentik, Authelia, etc. (Tested with Authentik.)

### Notifications & Integrations

**Dashboard View**: If you’re on a larger screen (like a laptop or tablet) and logged in as an admin, DayDan shows a mount-friendly dashboard layout. a full task list, calendar, and recent activity all in one place. Perfect for wall-mounted displays or shared tablets. With the ability for any user to pick their account and complete a task on the go!

**Realtime Sync**: Enable realtime sync to instantly reflect task changes across all connected devices and users.  whether you are adding, editing, or completing a task. It reflects immediately on enabled devices!

**Offline Support**: You can access DayDan if you lose connection and navigate some areas, but this is very limited functionality at the moment. 

**Multi-Platform Notifications**: Get reminders through the mobile app (we have an alpha iOS app on TestFlight, and the Android APK is available in releases), as well as via Telegram, Discord, or Pushover.

**Home Assistant Integration**: Manage and view tasks directly within Home Assistant using the official integration. It creates separate to-do lists for each DayDan user. DayDan Home Assistant Integration

### Developer & API Features
**REST API**: Full access to DayDan’s features through a REST API, great for custom automations and integrations. (For external use, we recommend using the eAPI, which offers limited access intended for long-lived access tokens.)

**Webhook System**: Connect DayDan to external systems using flexible webhook support good for custom notification flows or automations.

---

## Quick Start
DayDan works with secure built-in defaults. No YAML file, database selection,
JWT secret, CORS list, or scheduler/realtime tuning is required.

### Using Docker Compose

Prerequisites: Docker (with Compose v2.17+). No Node, npm, Go, or Vite is
needed on the host — the image build compiles the frontend and backend
internally.

DayDan's frontend and backend live in two repositories. Clone them side by
side (the Compose file expects the frontend at `../daydan-app`; set
`DAYDAN_FRONTEND_CONTEXT` to override the path):

```bash
git clone <daydan-app-url> daydan-app
git clone <daydan-server-url> daydan-server
cd daydan-server
docker compose up -d --build
```

The included Compose file builds DayDan, exposes port 2021, mounts one
persistent data volume, and starts automatically on boot (`restart:
unless-stopped`). The frontend bundle uses same-origin relative `/api` URLs,
so the deployment works unchanged over a LAN IP, hostname, reverse proxy, or
HTTPS domain — changing the server address never requires rebuilding.

To update an existing deployment:

```bash
git pull                       # in both repos
docker compose up -d --build
```

Persistent runtime state lives in `/data` inside the container:


- `/data/daydan.db` — SQLite database (legacy `taskier.db` / `donetick.db` are migrated automatically)
- `/data/jwt-secret` — automatically generated session-signing secret
- `/data/uploads` — local attachments

The backend serves both the embedded frontend and `/api/...` from the same
origin. On upgrades, keep the same data volume and restart the container.

### Reverse-proxy deployments

Forwarding headers are honored only from allowlisted proxies
(`server.trusted_proxies`, loopback by default), so directly exposed instances
cannot be spoofed with `X-Forwarded-*` headers. For Nginx/Caddy/Traefik
configuration — including header sanitization requirements and the
short-lived access-token policy — see [docs/reverse-proxy.md](docs/reverse-proxy.md).

The default Compose volume is `daydan-data`. Existing Taskier deployments can
reuse a `taskier-data` volume without copying data:

```bash
DAYDAN_DOCKER_VOLUME=taskier-data docker compose up -d
```

Legacy `TASKIER_DOCKER_VOLUME` is also accepted as a fallback override.

### Operating the deployment

```bash
docker compose logs -f          # follow logs
docker compose restart          # restart the container
docker compose down             # stop (data volume is kept)
docker compose up -d            # start again
```

Back up all persistent state with:

```bash
docker run --rm -v daydan-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/daydan-data-backup.tar.gz -C /data .
```

Restore by extracting the archive into an empty `daydan-data` volume before
starting the container.

### Release provenance

Official images are built by `.github/workflows/go-release.yml`, which checks
out this repository at the release tag and `daydan-app` from the same owner.
The two repositories do **not** share synchronized version tags: by default a
release pairs with `daydan-app`'s `develop` branch, and maintainers must pass
the workflow's `frontend_ref` input to pin a specific frontend tag, branch, or
commit for a release. The embedded frontend commit is recorded in the image
label `com.daydan.frontend.commit` and in each build's run summary.

Configuration remains optional. Canonical `DAYDAN_*` environment variables are
preferred; legacy `DT_*`, `TASKIER_*`, and `DONETICK_*` variables remain
compatible. Advanced users can set `DAYDAN_DATA_DIR`, `DAYDAN_CONFIG_FILE`, or
use the minimal example in `config/daydan.example.yaml` (legacy alias:
`config/taskier.example.yaml`); PostgreSQL and external integrations remain
optional overrides.

### Using Docker

```bash
docker build -t daydan .
docker run -d --name daydan -p 2021:2021 -v daydan-data:/data daydan
```

### Using the Binary
1. **Download the latest release** from the [Releases](https://github.com/donetick/donetick/releases) page.
2. **Extract the file** and navigate to the folder:
   ```bash
   cd path/to/extracted-folder
   ```
3. **Run DayDan:**
   ```bash
   ./taskier
   ```

---



## Development Environment

### Build the frontend

1. Clone the frontend repository:
   ```bash
   git clone https://github.com/donetick/frontend.git donetick-frontend
   ```
2. Navigate to the frontend directory:
   ```bash
   cd donetick-frontend
   ```
3. Install dependencies:
   ```bash
   npm install
   ```
4. Build the frontend:
   ```bash
   npm run build
   ```
   The production bundle uses same-origin relative `/api` URLs by default, so
   it works against any self-hosted address without a special build mode. If
   you host the API on a different origin, set `VITE_APP_API_URL` at build
   time instead.
5. If you want to work on the frontend you can run:
   ```bash
   npm start
   ```


### Build the application

1. Clone the repository:
   ```bash
   git clone https://github.com/donetick/donetick.git
   ```
2. Navigate to the project directory:
   ```bash
   cd donetick
   ```
3. Install dependencies:
   ```bash
   go mod download
   ```
4. Copy the frontend build to the application:
   ```bash
   rm -rf ./frontend/dist
   cp -r ../donetick-frontend/dist ./frontend
   ```
5. Run the app locally; configuration and JWT setup are automatic:
   ```bash
   go run .
   ```
   Or build the application:
   ```bash
   go build -o donetick .
   ```

### Build the development Docker image

> Make sure to build the frontend and the app first before building the Docker image.

1. Build the Docker image:
   ```bash
   docker build -t donetick/donetick -f Dockerfile.dev .
   ```

---

## Single-Circle Instance Mode & OIDC Group-Based Roles

These features are designed for self-hosted single-household deployments where circle management should be hidden from end users and roles should be managed via your OIDC identity provider.

### Environment Variables

| Variable | Type | Default | Description |
|---|---|---|---|
| `DT_SINGLE_CIRCLE_INSTANCE` | bool | `false` | Hides circle-management UI and disables join/leave/delete-member/accept-request endpoints. New OIDC users are added to a shared household circle (ID 1) instead of getting a personal circle. |
| `DT_OAUTH2_ADMIN_GROUPS` | comma-separated strings | *(empty)* | OIDC group names that grant the `admin` role. |
| `DT_OAUTH2_MANAGER_GROUPS` | comma-separated strings | *(empty)* | OIDC group names that grant the `manager` role. |
| `DT_DISABLE_PASSWORD_AUTH` | bool | `false` | SSO-only: disables password login. |

### Role Resolution Rules

Role sync runs **on every OIDC login** (not just first-time user creation), so your IdP is the source of truth. Removing someone from the admin group in your IdP will demote them on their next login.

- If user is in any **admin group** -> `admin` (admin takes priority over manager)
- Else if user is in any **manager group** -> `manager`
- Else -> `member`
- If **neither** `DT_OAUTH2_ADMIN_GROUPS` **nor** `DT_OAUTH2_MANAGER_GROUPS` is configured, roles are not modified at all (preserves existing behavior).

Group matching is **exact-string and case-sensitive**.

### Example: Authentik Configuration

1. Create groups in Authentik (e.g., `donetick-admins`, `donetick-managers`).
2. Ensure your Authentik OAuth2 provider includes the `groups` scope and that the `groups` claim is returned in the userinfo endpoint.
3. Set the environment variables:
   ```yaml
   environment:
     - DT_SINGLE_CIRCLE_INSTANCE=true
     - DT_OAUTH2_ADMIN_GROUPS=donetick-admins
     - DT_OAUTH2_MANAGER_GROUPS=donetick-managers
     - DT_DISABLE_PASSWORD_AUTH=true
   ```

---

## Contributing
Contributions are welcome! If you want to work on something that is not already listed as an open, tagged issue, please open a [Discussion](https://github.com/donetick/donetick/discussions)  or reach out on [Discord](https://discord.gg/yyRDcwd3) first to ensure it aligns with our goals and to avoid any unnecessary effort.

**Please note:** Pull Requests that are submitted without prior discussion or do not address an existing tagged issue will be deprioritized in the review queue.

## Code Formatting and Linting

We use `golangci-lint` and `ruleguard` for linting and formatting. CI enforces lint checks on all pull requests.

To run lint checks locally, install the pinned version:
```
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
go install github.com/quasilyte/go-ruleguard/cmd/ruleguard@v0.4.5
```

Run lint checks:
```
golangci-lint run
```

Apply safe fixes:
```
golangci-lint run --fix
```

---

## License

This project is licensed under the **AGPLv3**. See the [LICENSE](LICENSE.md) file for more details.

---

## Join the Discussion
For ideas or feature requests, please use GitHub Discussions. We also have a Discord server and a subreddit for those who prefer those platforms!


[![Discord](https://img.shields.io/discord/1272383484509421639)](https://discord.gg/6hSH6F33q7)
[![Reddit](https://img.shields.io/reddit/subreddit-subscribers/donetick)](https://www.reddit.com/r/donetick)

[![Github Discussion](https://img.shields.io/github/discussions/donetick/donetick)](https://github.com/donetick/donetick/discussions)

---

## Support DayDan

 If you find it helpful, consider supporting us by starring the repository, contributing code, or sharing feedback!  

---
