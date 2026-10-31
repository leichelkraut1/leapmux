# LeapMux native Claude control

This plugin enrolls the existing Claude chat with the LeapMux Worker running as the same operating-system user. It sends only live turn identifiers, connection metadata, and command delivery outcomes to the private local bridge. Conversation viewing continues through LeapMux's separate read-only history reader.

Remote input starts a turn in this same conversation when it becomes idle. An explicit interrupt targets the exact turn displayed by LeapMux. The plugin preserves Claude's permission mode and approval prompts; it does not approve tools, plans, workspace trust, or MCP access. After a local Stop, send a message in the original chat before remote input can continue.

Load the Worker-generated plugin directory with `claude --plugin-dir <directory>` for a single new test session. A normal existing chat requires deliberate plugin installation followed by `/reload-plugins` in that chat. Installing this plugin does not transfer ownership or resume another process. This bridge requires CLI or Desktop Code 2.1.287 or later, or VS Code 2.1.299 or later, and a verifiable native owner. Desktop WSL sessions do not currently support plugins.

For deliberate installation, add this computer's generated bridge directory as a local marketplace with `claude plugin marketplace add <bridge-directory>`, then install `leapmux-native-control@leapmux-local-native-control`. Run `/reload-plugins` in each existing CLI, Desktop Code, or VS Code chat you want to enroll; future chats load the user-scoped plugin normally. Keep the original chat open. This is an enrollment step, not a restart or conversation transfer. Managed organization policy may prevent loading a user plugin.

The Worker creates the descriptor and plugin files in a private directory. The descriptor contains a local bearer credential: do not publish or copy it to another computer. Set up each computer's own Worker and plugin instead. This plugin is inactive inside LeapMux-managed Claude agents.
