# What's new

Written for the people who use Atlas Commander rather than the people who build
it. One short line per change, no jargon — this is what the update prompt shows.

## 0.1.1

- A Simple layout for new commanders: your agents as cards, with Approvals and Settings; switch to Advanced in Settings
- Existing setups keep the Advanced layout
- Smoother pages and sidebar, and running agents' status dots gently pulse (they follow your desktop's animation setting)
- The Board's summary line no longer runs under the New agent button in a narrow window
- Menus and dropdown lists have rounded corners, and dropdowns open below the choice
- The Dispatch to… button shows a proper arrow
- Kill all no longer keeps a focus outline after you click it
- Settings no longer runs off the right edge when Commander keeps its files in a long folder path
- When Commander finds an update as it opens, it tells you once with what's new, and you can update now or later
- A Linux release download installed with install.sh can now update itself (the first update downloads and builds the source)
- If an update needs tools you don't have, Commander names them and the command that installs them
- For a copy installed by your package manager, the update window can run the update command in a terminal
- Settings, About shows how your copy was installed and so who updates it

## 0.1.0

- First beta: run a fleet of Claude agents and watch them all from one window
- A Board of every agent with its status, model, cost and last tool call
- Approvals: Claude Code asks you before it runs a tool, and you answer from Commander
- Tasks you can queue, prioritise and chain, with cost caps for each agent and fleet
- Analytics for cost, tokens and success rate, and an audit log that can only be added to
- Observed view of Claude Code sessions you started somewhere else, read-only
- Themes, with a theme editor; runs on Linux and Windows
- Agents can now use OpenAI, Gemini or a local model through Ollama, as well as Claude Code
- The agent editor lists the models each provider offers
- The Claude API provider is gone; agents that used it need another provider chosen
- Updates: Commander checks for a newer version when it opens and can update itself when built from source
- Settings has an Updates section to check now, pick the Release or Beta channel, or turn the check off
- Phone access, off until you turn it on in Settings: watch your agents and answer approvals from your phone
- An Android app to pair with Commander: see the board, read transcripts, and allow or deny approvals from a notification
