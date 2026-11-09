import { afterEach, describe, expect, it, vi } from 'vitest'
import { convertImageToWebp } from '@/lib/image-webp'

afterEach(() => {
  vi.unstubAllGlobals()
})

function sourceFile(size = 100) {
  return new File([new Uint8Array(size)], 'vacation.photo.png', { type: 'image/png', lastModified: 123 })
}

function stubConversion({
  width,
  height,
  outputType = 'image/webp',
  outputSize = 20,
}: {
  width: number
  height: number
  outputType?: string
  outputSize?: number
}) {
  const bitmap = { width, height, close: vi.fn() }
  const createImageBitmapMock = vi.fn().mockResolvedValue(bitmap)
  const drawImage = vi.fn()
  const convertToBlob = vi.fn().mockResolvedValue(new Blob([new Uint8Array(outputSize)], { type: outputType }))
  const canvases: Array<{ width: number; height: number }> = []

  class OffscreenCanvasStub {
    width: number
    height: number

    constructor(canvasWidth: number, canvasHeight: number) {
      this.width = canvasWidth
      this.height = canvasHeight
      canvases.push({ width: canvasWidth, height: canvasHeight })
    }

    getContext() {
      return { drawImage }
    }

    convertToBlob = convertToBlob
  }

  vi.stubGlobal('createImageBitmap', createImageBitmapMock)
  vi.stubGlobal('OffscreenCanvas', OffscreenCanvasStub)
  return { bitmap, canvases, convertToBlob, createImageBitmapMock, drawImage }
}

describe('convertImageToWebp', () => {
  it('按长边缩放并输出 webp', async () => {
    const file = sourceFile()
    const { bitmap, canvases, convertToBlob, createImageBitmapMock, drawImage } = stubConversion({ width: 5120, height: 2560 })

    const converted = await convertImageToWebp(file)

    expect(createImageBitmapMock).toHaveBeenCalledWith(file)
    expect(canvases).toEqual([{ width: 2560, height: 1280 }])
    expect(drawImage).toHaveBeenCalledWith(bitmap, 0, 0, 2560, 1280)
    expect(convertToBlob).toHaveBeenCalledWith({ type: 'image/webp', quality: 0.82 })
    expect(converted).not.toBe(file)
    expect(converted.name).toBe('vacation.photo.webp')
    expect(converted.type).toBe('image/webp')
    expect(converted.lastModified).toBe(123)
    expect(converted.size).toBe(20)
    expect(bitmap.close).toHaveBeenCalledOnce()
  })

  it('图片小于长边限制时不放大，并应用自定义质量', async () => {
    const file = sourceFile()
    const { canvases, convertToBlob } = stubConversion({ width: 1200, height: 800 })

    await convertImageToWebp(file, { maxEdge: 2560, quality: 0.7 })

    expect(canvases).toEqual([{ width: 1200, height: 800 }])
    expect(convertToBlob).toHaveBeenCalledWith({ type: 'image/webp', quality: 0.7 })
  })

  it('浏览器不支持 webp 编码时返回原文件', async () => {
    const file = sourceFile()
    stubConversion({ width: 5120, height: 2560, outputType: 'image/png' })

    await expect(convertImageToWebp(file)).resolves.toBe(file)
  })

  it.each([100, 101])('转码后体积不比原文件小（%s 字节）时返回原文件', async (outputSize) => {
    const file = sourceFile()
    stubConversion({ width: 5120, height: 2560, outputSize })

    await expect(convertImageToWebp(file)).resolves.toBe(file)
  })

  it('解码失败时返回原文件', async () => {
    const file = sourceFile()
    vi.stubGlobal('createImageBitmap', vi.fn().mockRejectedValue(new Error('decode failed')))

    await expect(convertImageToWebp(file)).resolves.toBe(file)
  })
})
