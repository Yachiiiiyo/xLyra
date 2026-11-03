import type { AgentGeneratedImage } from '@/features/agent/api/agent'
import type { AgentRun } from '@/features/agent/lib/agent-events'

export function isGeneratedImagePath(src?: string): boolean {
  return Boolean(src && /^images\/[^/\\]+$/.test(src) && !src.includes('..'))
}

export function runGeneratedImages(run: AgentRun): AgentGeneratedImage[] {
  const seen = new Set<string>()
  const images: AgentGeneratedImage[] = []
  for (const step of run.steps) {
    for (const image of step.images ?? []) {
      if (seen.has(image.path)) continue
      seen.add(image.path)
      images.push(image)
    }
  }
  return images
}
