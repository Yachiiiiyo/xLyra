const HTTP_URL = /https?:\/\/[^\s"'<>]+/gi

export function sanitizeErrorText(message: string): string {
  return message.replace(HTTP_URL, '…')
}
