import { describe, expect, it } from 'vitest'
import { parseDelimited } from './csv'

describe('parseDelimited', () => {
  it('解析分隔符、CRLF 与空字段', () => {
    expect(parseDelimited('a,b,\r\n1,2,3', ',')).toEqual([['a', 'b', ''], ['1', '2', '3']])
    expect(parseDelimited('a\tb\n1\t2', '\t')).toEqual([['a', 'b'], ['1', '2']])
  })

  it('支持引号内分隔符、换行与双引号转义', () => {
    expect(parseDelimited('"a,b","x""y"\r\n"1\n2",z', ',')).toEqual([['a,b', 'x"y'], ['1\n2', 'z']])
  })
})
