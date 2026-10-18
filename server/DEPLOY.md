# Backend deploys
- Render service: Hora_light_MVP (Go, root directory `server/`, branch `main`).
- Auto-deploy: on commit to `main`, only when files under `server/` change. Commits that touch only `app/` or `mobile/` do not redeploy the backend.
- Build: `go build -o app .` · Start: `./app`
- During App Store review, merge to `main` only changes that do not alter existing API behavior, billing, or the review sandbox account. Develop on branches.
