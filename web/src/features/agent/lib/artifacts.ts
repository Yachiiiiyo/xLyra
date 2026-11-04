import type { AgentArtifactFile, AgentArtifacts } from '@/features/agent/api/agent'

export function agentGeneratedFileCount(artifacts: AgentArtifacts | null | undefined): number {
  return artifacts ? artifacts.files.filter((file) => file.agent_generated).length : 0
}

/** Agent 产生的在前,同组内按修改时间倒序 */
export function sortArtifactFiles(files: AgentArtifactFile[]): AgentArtifactFile[] {
  const time = (file: AgentArtifactFile) => {
    const parsed = Date.parse(file.mtime)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return [...files].sort((left, right) => Number(right.agent_generated) - Number(left.agent_generated) || time(right) - time(left))
}
