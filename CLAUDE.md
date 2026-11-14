# Coding preferences

- Always prefer human-readable code: clear names, simple control flow, and comments where intent isn't obvious.
- Always prefer Go (golang) unless the task clearly requires another language.
- Organize code into modules/packages instead of large single files.
- Keep each file or module to a maximum of around 1000 lines; split it when it grows beyond that.

# Project requirements

- All work on ORB must follow [requirements.md](requirements.md). Read it before designing or changing code.
- If a requirement needs to change, update requirements.md in the same change as the code.
- Reference requirement IDs (for example `LAT-2`) in code comments, commits and reviews where relevant.
- Every commit must be signed off for the DCO (`git commit -s`), and every new source file needs the license header from `LIC-3` (`make license-headers-fix`).
