export type ChatAttachment = {
  id: string
  name: string
  mimeType: string
  size: number
  dataURL?: string
  assetId?: string
  src?: string
}

export type GatewayModel = {
  id: string
  mappedModel?: string
  displayName: string
  category: string
  ownedBy?: string
  endpointTypes: string[]
  siteTypes?: string[]
  reasoning?: GatewayModelReasoning
}

export type ReasoningEffort = 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra'

/** Server-declared reasoning effort spec for a model (from protocol_specs.json via the gateway). */
export type GatewayModelReasoning = {
  levels: ReasoningEffort[]
  thinkingMandatory?: boolean
}
