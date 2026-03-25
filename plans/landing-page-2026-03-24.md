# Landing Page for hermagent.com

Deploy a polished, product-grade landing page for herm at hermagent.com via GitHub Pages + GitHub Actions.

## Constraints & Decisions

- **No frameworks** — pure HTML/CSS/JS, assembled and deployed by a GitHub Action
- **Locally testable** — opening `docs/index.html` in a browser must work (no build step needed for dev)
- **GitHub Pages deployment** — same pattern as LangDag: `actions/upload-pages-artifact` → `actions/deploy-pages`
- **Custom domain** — hermagent.com via CNAME file in `docs/`
- **GitHub stars** — fetched client-side via GitHub API (`GET /repos/aduermael/herm`) and displayed prominently
- **Version injection** — the workflow injects the latest git tag into the page (like LangDag does with `sed`)

## Existing Assets

- `img/demo.gif` / `img/demo.mp4` — terminal demo (use on the page)
- `README.md` — copy source for features, install instructions, FAQ
- Hermit crab branding ("Herm" = hermit crab, hermetic = sealed/safe)

## Design Direction

This is a **product landing page**, not a library docs page. It should feel confident and polished:
- Dark theme with vibrant accent gradients (think: terminal aesthetic meets modern SaaS)
- Hero section with a bold tagline, demo GIF/video, and CTA buttons (Install / GitHub)
- Animated star count badge near the hero or header
- Feature cards highlighting the 4 pillars: containerized, multi-provider, self-building devenvs, open-source
- Install section with copyable commands (curl, brew, source)
- Comparison section (vs Claude Code / OpenCode / Pi) — concise, not aggressive
- FAQ section (accordion or expandable)
- Footer with Discord link, GitHub link, MIT license note
- Smooth scroll, subtle animations (CSS only — no heavy JS libs)
- Responsive down to mobile
- The demo GIF/video should be a centerpiece — it's the strongest proof of what herm does

## Phase 1: Page Structure & Workflow

- [x] 1a: Create `docs/` directory with `index.html` containing the full landing page (HTML + inline CSS + inline JS). Include all sections: header/nav, hero with demo, features, install, comparison, FAQ, footer. Use `__VERSION__` placeholder for version injection. Use `__STARS__` as a fallback that gets replaced by JS at runtime via the GitHub API.
- [x] 1b: Add `docs/CNAME` file with `hermagent.com` for custom domain configuration
- [x] 1c: Create `.github/workflows/pages.yml` — GitHub Action that deploys `docs/` to GitHub Pages on push to main and version tags. Inject latest git tag version via `sed`. Follow the same pattern as LangDag's workflow (checkout with tags → sed version → configure-pages → upload artifact → deploy).

## Phase 2: Assets & Polish

- [ ] 2a: Copy `img/demo.gif` into `docs/img/` so the page can reference it with a relative path (works both locally and deployed). Also copy `demo.mp4` for a potential video element with GIF fallback.
- [ ] 2b: Add a `docs/favicon.ico` or SVG favicon (simple hermit crab shell icon, can be a minimal SVG inline in the HTML head if preferred)
- [ ] 2c: Add Open Graph / Twitter Card meta tags to `index.html` for social sharing previews (title, description, image pointing to the demo GIF or a dedicated OG image)

## Phase 3: Testing & Verification

- [ ] 3a: Verify the page opens correctly from `docs/index.html` in a browser (check relative paths, no broken references, GitHub stars fetch works with CORS). Fix any issues found.
- [ ] 3b: Verify the GitHub Action workflow is valid YAML and the deployment steps are correct. Do a dry-run check of the sed version injection logic.

## Success Criteria

- Opening `docs/index.html` locally renders the full page with all sections
- GitHub stars display (may show fallback text if CORS blocks local file:// requests — that's OK)
- The GitHub Action workflow is valid and follows the proven LangDag pattern
- Page is responsive and looks good on mobile
- Version number appears on the page after workflow injection
- hermagent.com CNAME is configured for GitHub Pages
