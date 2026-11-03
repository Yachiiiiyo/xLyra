import { describe, expect, it } from 'vitest'
import { isGeneratedImagePath, runGeneratedImages } from '@/features/agent/lib/generated-images'
import type { AgentRun } from '@/features/agent/lib/agent-events'

function image(path: string, alt?: string) {
  return { path, mime: 'image/png', bytes: 1, alt }
}

describe('isGeneratedImagePath', () => {
  it('只接受 images 下单层工作区图片路径', () => {
    expect(isGeneratedImagePath('images/a.png')).toBe(true)
    for (const path of ['../a.png', 'images/sub/a.png', 'http://x/a.png', '/images/a.png', 'images\\a.png', '', 'images/../a.png', undefined]) {
      expect(isGeneratedImagePath(path)).toBe(false)
    }
  })
})

describe('runGeneratedImages', () => {
  it('按步骤顺序收集并按 path 去重', () => {
    const run: AgentRun = {
      id: 'run', status: 'done', finalText: '', permissions: [], startedAt: 0,
      steps: [
        { id: '1', kind: 'tool', title: 'image_generation', status: 'done', images: [image('images/a.png', 'first'), image('images/b.png')] },
        { id: '2', kind: 'tool', title: 'image_generation', status: 'done', images: [image('images/a.png', 'duplicate'), image('images/c.png')] },
      ],
    }
    expect(runGeneratedImages(run).map((item) => item.path)).toEqual(['images/a.png', 'images/b.png', 'images/c.png'])
    expect(runGeneratedImages(run)[0]?.alt).toBe('first')
  })
})
