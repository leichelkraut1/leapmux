import { claudeTest } from '../claude-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectDecodedImageInBubble, expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'
import { messageBubbles } from '../helpers/ui'

claudeTest.describe('Claude Code images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result, so the `img`
  // is not something the prompt or the scripted reply can fake.
  claudeTest('a Read of a PNG draws the picture in the tool row', async ({ native, authenticatedClaudeWorkspace }) => {
    const { resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedClaudeWorkspace.workingDir,
      marker: 'claude-42',
      toolCall: image => readToolCall(native.provider, 'read-png', image.fileName),
    })
    // An Anthropic tool result carries the image block of the Read, so the next model request holds the PNG.
    expectPngInRequest(resultRequest)
    // The picture is visible before helpers can open any folded activity.
    await expectDecodedImageInBubble(messageBubbles(native.page)
      .filter({ has: native.page.locator('button[aria-label="Open image"] img') }).last())
    await expectToolRowImage(native.page, 'tool-image-claude-42')
  })
})
