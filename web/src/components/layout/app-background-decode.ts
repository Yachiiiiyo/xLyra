export const decodedBackgroundImages = new Set<string>()

export async function decodeAppBackgroundImage(image: string) {
  if (decodedBackgroundImages.has(image)) return
  const element = new Image()
  element.src = image
  try {
    await element.decode()
  } catch {
    // 解码失败也按已加载处理，避免背景层永久透明。
  }
  decodedBackgroundImages.add(image)
}
