type ImageSource = ImageBitmap | HTMLImageElement

export async function convertImageToWebp(
  file: File,
  options?: { maxEdge?: number; quality?: number },
): Promise<File> {
  const maxEdge = options?.maxEdge ?? 2560
  const quality = options?.quality ?? 0.82
  let bitmap: ImageBitmap | undefined
  let objectURL: string | undefined

  try {
    let source: ImageSource
    if (typeof createImageBitmap === 'function') {
      bitmap = await createImageBitmap(file)
      source = bitmap
    } else {
      objectURL = URL.createObjectURL(file)
      const image = new Image()
      image.src = objectURL
      await image.decode()
      source = image
    }

    const sourceWidth = 'naturalWidth' in source ? source.naturalWidth : source.width
    const sourceHeight = 'naturalHeight' in source ? source.naturalHeight : source.height
    const scale = Math.min(1, maxEdge / Math.max(sourceWidth, sourceHeight))
    const width = Math.max(1, Math.round(sourceWidth * scale))
    const height = Math.max(1, Math.round(sourceHeight * scale))
    const blob = await encodeWebp(source, width, height, quality)

    if (blob.type !== 'image/webp' || blob.size >= file.size) return file
    const name = `${file.name.replace(/\.[^.]+$/, '')}.webp`
    return new File([blob], name, { type: 'image/webp', lastModified: file.lastModified })
  } catch {
    return file
  } finally {
    bitmap?.close()
    if (objectURL) URL.revokeObjectURL(objectURL)
  }
}

async function encodeWebp(source: ImageSource, width: number, height: number, quality: number): Promise<Blob> {
  if (typeof OffscreenCanvas === 'function') {
    const canvas = new OffscreenCanvas(width, height)
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas 2D is unavailable')
    context.drawImage(source, 0, 0, width, height)
    return canvas.convertToBlob({ type: 'image/webp', quality })
  }

  const canvas = document.createElement('canvas')
  canvas.width = width
  canvas.height = height
  const context = canvas.getContext('2d')
  if (!context) throw new Error('Canvas 2D is unavailable')
  context.drawImage(source, 0, 0, width, height)
  return new Promise<Blob>((resolve, reject) => {
    canvas.toBlob(
      (blob) => blob ? resolve(blob) : reject(new Error('WebP encoding failed')),
      'image/webp',
      quality,
    )
  })
}
