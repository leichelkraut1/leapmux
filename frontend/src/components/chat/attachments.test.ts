import type { FileAttachment } from './attachments'
import type { AttachmentCapabilities } from './providers/capabilities'
import { describe, expect, it } from 'vitest'
import { MAX_AGENT_INPUT_ITEM_BYTES } from '~/generated/contracts/agent-input'
import attachmentClassificationCorpus from '../../../../testdata/attachment_classification_conformance.json'
import {
  attachmentUsesWorkerFile,
  buildAcceptAttribute,
  clearAttachments,
  collectDroppedAttachmentFiles,
  describeUnsupportedAttachment,
  forgetAgentAttachments,
  getAttachments,
  inferAttachmentDetails,
  isAttachmentSupported,
  isImageMimeType,
  MAX_TOTAL_ATTACHMENT_SIZE,
  nextPastedImageName,
  readFileAsAttachment,
  setAttachments,
  totalAttachmentSize,
} from './attachments'
import { queueEditDraftKey } from './composer/draftIdentity'

const claudeCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: true,
  binary: true,
  fileBackedKinds: ['binary'],
}

const codexCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: true,
  binary: true,
  fileBackedKinds: ['pdf', 'binary'],
}

const textAndImageCapabilities: AttachmentCapabilities = {
  text: true,
  image: true,
  pdf: false,
  binary: false,
}

// The Worker replays the same bytes and expected classification from this corpus.
describe('attachment classification conformance', () => {
  it.each(attachmentClassificationCorpus.cases)('classifies $name consistently with the Worker', (testCase) => {
    const data = Uint8Array.from(atob(testCase.dataBase64), character => character.charCodeAt(0))
    expect(inferAttachmentDetails(testCase.filename, testCase.mimeType, data)).toEqual({
      kind: testCase.expectedKind,
      mimeType: testCase.expectedMimeType,
    })
  })
})

describe('inferAttachmentDetails', () => {
  it('accepts image types', () => {
    expect(inferAttachmentDetails('test.png', 'image/png', new Uint8Array([137, 80, 78, 71]))).toEqual({
      kind: 'image',
      mimeType: 'image/png',
    })
  })

  it('accepts pdf', () => {
    expect(inferAttachmentDetails('test.pdf', 'application/pdf', new Uint8Array([37, 80, 68, 70]))).toEqual({
      kind: 'pdf',
      mimeType: 'application/pdf',
    })
  })

  it('infers text mime from extension when browser omits it', () => {
    expect(inferAttachmentDetails('styles.css', '', new TextEncoder().encode('body {}'))).toEqual({
      kind: 'text',
      mimeType: 'text/css',
    })
  })

  it('treats extensionless utf8 files as text/plain', () => {
    expect(inferAttachmentDetails('README', '', new TextEncoder().encode('hello'))).toEqual({
      kind: 'text',
      mimeType: 'text/plain',
    })
  })

  it.each([
    ['Control.L5X', '<RSLogix5000Content/>'],
    ['routine.st', 'IF Start THEN Motor := TRUE; END_IF;'],
    ['script.ps1', 'Write-Output ready'],
    ['custom.any-extension', 'plain text'],
  ])('accepts utf8 content from %s without a browser mime type', (filename, text) => {
    expect(inferAttachmentDetails(filename, '', new TextEncoder().encode(text)).kind).toBe('text')
  })

  it.each([
    ['drawing.PNG', 'image/png'],
    ['photo.jpg', 'image/jpeg'],
    ['document.PDF', 'application/pdf'],
  ])('preserves native media classification for %s with no browser mime', (filename, mimeType) => {
    expect(inferAttachmentDetails(filename, '', new Uint8Array([0, 255, 1])).mimeType).toBe(mimeType)
  })
  it.each([0x00, 0x01, 0x08, 0x0B, 0x0E, 0x1F, 0x7F])('keeps byte %s out of text attachments even with a text mime', (control) => {
    const data = new Uint8Array([0x61, control, 0x62])
    expect(inferAttachmentDetails('custom.file', '', data).kind).toBe('binary')
    expect(inferAttachmentDetails('notes.txt', 'text/plain', data).kind).toBe('binary')
  })

  it('preserves ordinary whitespace and ANSI escape logs as text', () => {
    const data = new TextEncoder().encode('log\tline\npage\f\r\x1B[32mready\x1B[0m')
    expect(inferAttachmentDetails('ansi.log', '', data).kind).toBe('text')
  })

  it('classifies unknown binary as binary', () => {
    expect(inferAttachmentDetails('archive.bin', '', new Uint8Array([0, 255, 1]))).toEqual({
      kind: 'binary',
      mimeType: 'application/octet-stream',
    })
  })
})

describe('attachmentUsesWorkerFile', () => {
  it.each([
    ['project.ACD', 'application/octet-stream'],
    ['document.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
    ['workbook.xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
    ['bundle.zip', 'application/zip'],
  ])('routes %s as a file without changing the bytes', (filename, mimeType) => {
    const data = new Uint8Array([0, 255, 1, 128])
    const attachment = { filename, mimeType, data }
    expect(attachmentUsesWorkerFile(attachment, claudeCapabilities)).toBe(true)
    expect(attachmentUsesWorkerFile(attachment, codexCapabilities)).toBe(true)
    expect(attachmentUsesWorkerFile(attachment, textAndImageCapabilities)).toBe(false)
    expect(data).toEqual(new Uint8Array([0, 255, 1, 128]))
  })

  it('keeps Claude pdf native and marks Codex pdf for tools', () => {
    const attachment = {
      filename: 'drawing.pdf',
      mimeType: 'application/pdf',
      data: new Uint8Array([37, 80, 68, 70]),
    }
    expect(attachmentUsesWorkerFile(attachment, claudeCapabilities)).toBe(false)
    expect(attachmentUsesWorkerFile(attachment, codexCapabilities)).toBe(true)
  })

  it('does not mark native text or images as files for tools', () => {
    const text = { filename: 'control.L5X', mimeType: '', data: new TextEncoder().encode('<Project/>') }
    const image = { filename: 'photo.png', mimeType: 'image/png', data: new Uint8Array([137, 80, 78, 71]) }
    expect(attachmentUsesWorkerFile(text, codexCapabilities)).toBe(false)
    expect(attachmentUsesWorkerFile(image, codexCapabilities)).toBe(false)
  })
})

describe('isImageMimeType', () => {
  it('returns true for image types', () => {
    expect(isImageMimeType('image/png')).toBe(true)
    expect(isImageMimeType('image/jpeg')).toBe(true)
  })

  it('returns false for non-image types', () => {
    expect(isImageMimeType('application/pdf')).toBe(false)
    expect(isImageMimeType('text/plain')).toBe(false)
  })
})

describe('attachment support', () => {
  it('allows text across Claude and Codex', () => {
    expect(isAttachmentSupported('text', claudeCapabilities)).toBe(true)
    expect(isAttachmentSupported('text', codexCapabilities)).toBe(true)
  })

  it('accepts arbitrary files for Claude and Codex without widening other providers', () => {
    for (const capabilities of [claudeCapabilities, codexCapabilities]) {
      expect(isAttachmentSupported('pdf', capabilities)).toBe(true)
      expect(isAttachmentSupported('binary', capabilities)).toBe(true)
    }
    expect(isAttachmentSupported('pdf', textAndImageCapabilities)).toBe(false)
    expect(isAttachmentSupported('binary', textAndImageCapabilities)).toBe(false)
  })

  it('builds provider-specific rejection copy', () => {
    expect(describeUnsupportedAttachment('pdf', 'Pi')).toBe('Pi does not support PDF attachments')
    expect(describeUnsupportedAttachment('binary', 'Pi')).toBe('Pi does not support binary attachments')
  })
})

describe('buildAcceptAttribute', () => {
  it('does not hide unknown text extensions in the picker', () => {
    expect(buildAcceptAttribute(textAndImageCapabilities)).toBeUndefined()
    expect(buildAcceptAttribute(claudeCapabilities)).toBeUndefined()
    expect(buildAcceptAttribute(codexCapabilities)).toBeUndefined()
  })

  it('retains a format filter for media-only providers', () => {
    const accept = buildAcceptAttribute({ text: false, image: true, pdf: true, binary: false })!
    expect(accept.split(',')).toEqual([
      'image/png',
      'image/jpeg',
      'image/gif',
      'image/webp',
      'application/pdf',
    ])
  })

  it('omits accept when provider supports arbitrary binary attachments', () => {
    expect(buildAcceptAttribute({ text: true, image: true, pdf: true, binary: true })).toBeUndefined()
  })
})

describe('totalAttachmentSize', () => {
  it('returns 0 for empty array', () => {
    expect(totalAttachmentSize([])).toBe(0)
  })

  it('sums attachment sizes', () => {
    const attachments = [
      { size: 100 } as FileAttachment,
      { size: 200 } as FileAttachment,
      { size: 300 } as FileAttachment,
    ]
    expect(totalAttachmentSize(attachments)).toBe(600)
  })
})

describe('max total attachment size', () => {
  it('is 10 MB', () => {
    // The limit comes from contracts/agent-input.json, so the Worker enforces
    // the same number. Restating the literal here would recreate the second
    // copy the contract exists to remove.
    expect(MAX_TOTAL_ATTACHMENT_SIZE).toBe(MAX_AGENT_INPUT_ITEM_BYTES)
  })
})

describe('attachment cache', () => {
  it('returns empty array for unknown agent', () => {
    expect(getAttachments('nonexistent')).toEqual([])
  })

  it('stores and retrieves attachments', () => {
    const attachments = [{ id: 'a1', filename: 'test.png' } as FileAttachment]
    setAttachments('agent-1', attachments)
    expect(getAttachments('agent-1')).toBe(attachments)
  })

  it('clears attachments', () => {
    setAttachments('agent-2', [{ id: 'a2' } as FileAttachment])
    clearAttachments('agent-2')
    expect(getAttachments('agent-2')).toEqual([])
  })

  it('forgets normal and queue-edit attachments for a closed agent', () => {
    setAttachments('agent-3', [{ id: 'normal' } as FileAttachment])
    setAttachments('agent-3-queue-input-1', [{ id: 'queued' } as FileAttachment])
    setAttachments('agent-30', [{ id: 'other' } as FileAttachment])

    forgetAgentAttachments('agent-3')

    expect(getAttachments('agent-3')).toEqual([])
    expect(getAttachments('agent-3-queue-input-1')).toEqual([])
    expect(getAttachments('agent-30')).toHaveLength(1)
  })
})

describe('queueEditDraftKey', () => {
  it('produces a key that the agent sweep forgets', () => {
    const key = queueEditDraftKey('agent-sweep', 'input-1')
    setAttachments(key, [{ id: 'queued' } as FileAttachment])

    forgetAgentAttachments('agent-sweep')

    expect(getAttachments(key)).toEqual([])
  })
})

const PASTED_IMAGE_RE = /^Pasted Image \d+$/

describe('nextPastedImageName', () => {
  it('increments counter per agent', () => {
    const name1 = nextPastedImageName('paste-agent-1')
    const name2 = nextPastedImageName('paste-agent-1')
    expect(name1).toMatch(PASTED_IMAGE_RE)
    expect(name2).toMatch(PASTED_IMAGE_RE)
    // Second should have a higher number
    const num1 = Number.parseInt(name1.replace('Pasted Image ', ''), 10)
    const num2 = Number.parseInt(name2.replace('Pasted Image ', ''), 10)
    expect(num2).toBe(num1 + 1)
  })
})

describe('readFileAsAttachment', () => {
  it('reads a file and returns attachment with Uint8Array data', async () => {
    const content = new Uint8Array([137, 80, 78, 71]) // PNG header bytes
    const file = new File([content], 'test.png', { type: 'image/png' })

    const attachment = await readFileAsAttachment(file)

    expect(attachment.id).toBeTruthy()
    expect(attachment.filename).toBe('test.png')
    expect(attachment.mimeType).toBe('image/png')
    expect(attachment.size).toBe(4)
    expect(attachment.data).toBeInstanceOf(Uint8Array)
    expect(attachment.data).toEqual(content)
    expect(attachment.file).toBe(file)
  })

  it('uses custom filename when provided', async () => {
    const file = new File(['hello'], 'original.txt', { type: 'image/png' })
    const attachment = await readFileAsAttachment(file, 'custom.png')
    expect(attachment.filename).toBe('custom.png')
  })

  it('infers text mime for files with empty browser mime', async () => {
    const file = new File(['name,value\nfoo,1\n'], 'report.csv', { type: '' })
    const attachment = await readFileAsAttachment(file)
    expect(attachment.mimeType).toBe('text/csv')
  })
})

/** Create a minimal DataTransfer-like object for testing. */
function mockDataTransfer(items: { webkitGetAsEntry: () => unknown }[]): DataTransfer {
  return { items, files: [] } as unknown as DataTransfer
}

describe('collectDroppedAttachmentFiles', () => {
  function createFileEntry(path: string, file: File) {
    const parts = path.split('/')
    const name = parts.at(-1) ?? file.name
    return {
      isFile: true,
      isDirectory: false,
      name,
      fullPath: `/${path}`,
      file: (resolve: (value: File) => void) => resolve(file),
    }
  }

  function createDirectoryEntry(name: string, entries: unknown[]) {
    return {
      isFile: false,
      isDirectory: true,
      name,
      fullPath: `/${name}`,
      createReader: () => {
        let done = false
        return {
          readEntries: (resolve: (value: unknown[]) => void) => {
            if (done) {
              resolve([])
              return
            }
            done = true
            resolve(entries)
          },
        }
      },
    }
  }

  it('recursively expands directory drops and preserves relative paths', async () => {
    const readme = createFileEntry('project/README.md', new File(['# test'], 'README.md', { type: 'text/markdown' }))
    const logo = createFileEntry('project/assets/logo.png', new File([new Uint8Array([1, 2, 3])], 'logo.png', { type: 'image/png' }))
    const assets = createDirectoryEntry('assets', [logo])
    const project = createDirectoryEntry('project', [assets, readme])
    const dataTransfer = mockDataTransfer([{ webkitGetAsEntry: () => project }])

    const result = await collectDroppedAttachmentFiles(dataTransfer)

    expect(result.sizeLimitHit).toBe(false)
    expect(result.files.map(file => file.filename)).toEqual([
      'project/assets/logo.png',
      'project/README.md',
    ])
  })

  it('stops traversal immediately once the size limit would be exceeded', async () => {
    const first = createFileEntry('big/first.txt', new File([new Uint8Array(64)], 'first.txt', { type: 'text/plain' }))
    const tooLarge = createFileEntry(
      'big/second.txt',
      new File([new Uint8Array(64)], 'second.txt', { type: 'text/plain' }),
    )
    const late = createFileEntry('big/late.txt', new File([new Uint8Array(64)], 'late.txt', { type: 'text/plain' }))
    const directory = createDirectoryEntry('big', [first, tooLarge, late])
    const dataTransfer = mockDataTransfer([{ webkitGetAsEntry: () => directory }])

    const result = await collectDroppedAttachmentFiles(dataTransfer, MAX_TOTAL_ATTACHMENT_SIZE - 96)

    expect(result.sizeLimitHit).toBe(true)
    expect(result.files.map(file => file.filename)).toEqual(['big/first.txt'])
  })
})
