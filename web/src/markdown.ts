import MarkdownIt from 'markdown-it'

// markdown renders Markdown text, escaping raw HTML and opening links in new tabs
export const markdown = new MarkdownIt({ linkify: true, breaks: true })
const renderLink =
  markdown.renderer.rules.link_open ??
  ((tokens, index, options, _env, self) => self.renderToken(tokens, index, options))
markdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  tokens[index]?.attrSet('target', '_blank')
  tokens[index]?.attrSet('rel', 'noreferrer')
  return renderLink(tokens, index, options, env, self)
}
