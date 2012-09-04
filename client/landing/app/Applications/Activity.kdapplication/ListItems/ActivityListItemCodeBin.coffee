class CodeBinActivityItemView extends ActivityItemChild

  constructor:(options, data)->
    options = $.extend
      cssClass    : "activity-item codebin"
      tooltip     :
        title     : "Code Bin"
        offset    : 3
        selector  : "span.type-icon"
    ,options
    super options,data

    codeBinHTMLData = @getData().attachments[0]
    codeBinCSSData = @getData().attachments[1]
    codeBinJSData = @getData().attachments[2]

    codeBinHTMLData.title = @getData().title
    codeBinCSSData.title = @getData().title
    codeBinJSData.title = @getData().title

    @codeBinHTMLView = new CodeSnippetView {}, codeBinHTMLData
    @codeBinCSSView = new CodeSnippetView {}, codeBinCSSData
    @codeBinJSView = new CodeSnippetView {}, codeBinJSData

    @codeBinResultView = new CodeBinResultView
      tagName: "iframe"
      attributes:
        src:"http://codepen.io/"

    ,data

    # log data.meta.tags
    # @tagGroup = new LinkGroup {
    #   group         : data.meta.tags
    #   itemsToShow   : 3
    #   subItemClass  : TagFollowBucketItemView
    # }

  render:->
    super()

    codeBinHTMLData = @getData().attachments[0]
    codeBinCSSData = @getData().attachments[1]
    codeBinJSData = @getData().attachments[2]

    codeBinHTMLData.title = @getData().title
    codeBinCSSData.title = @getData().title
    codeBinJSData.title = @getData().title

    @codeBinHTMLView.setData codeBinHTMLData
    @codeBinCSSView.setData codeBinCSSData
    @codeBinJSView.setData codeBinJSData

    @codeBinHTMLView.render()
    @codeBinCSSView.render()
    @codeBinJSView.render()


  click:(event)->
    super
    if $(event.target).is(".activity-item-right-col h3")
      appManager.tell "Activity", "createContentDisplay", @getData()

  viewAppended: ->
    return if @getData().constructor is bongo.api.CCodeBinActivity
    super()
    @setTemplate @pistachio()
    @template.update()

    maxHeight = 30
    views = [@codeBinJSView,@codeBinCSSView,@codeBinHTMLView]

    for view in views
      if view.getHeight()>maxHeight
        maxHeight = view.getHeight()

    @$("pre.subview").css height:maxHeight


  pistachio:->

    """
    {{> @settingsButton}}
    <span class="avatar">{{> @avatar}}</span>
    <div class='activity-item-right-col'>
      {h3{#(title)}}
      <p class='context'>{{@utils.applyTextExpansions #(body)}}</p>
      <div class="code-bin-source">
      {{> @codeBinHTMLView}}
      {{> @codeBinCSSView}}
      {{> @codeBinJSView}}
      </div>
      {{> @codeBinResultView}}
      <footer class='clearfix'>
        <div class='type-and-time'>
          <span class='type-icon'></span> by {{> @author}}
          {time{$.timeago #(meta.createdAt)}}
          {{> @tags}}
        </div>
        {{> @actionLinks}}
      </footer>
      {{> @commentBox}}
    </div>
    """

class CodeBinResultView extends KDCustomHTMLView
  constructor:(options,data)->
    options.cssClass = "result-container"
    super options, data
    data = @getData()

    @codeView = new KDCustomHTMLView
      tagName  : "code"
      pistachio : '{{#(content)}}'
    , data

  viewAppended: ->

    @setTemplate @pistachio()
    @template.update()


  pistachio:->
    """
    <div class='kdview'>
      {pre{> @codeView}}
    </div>
    """

class CodeSnippetView extends KDCustomHTMLView

  openFileIteration = 0

  constructor:(options, data)->
    options.tagName  = "figure"
    options.cssClass = "code-container"
    super
    @unsetClass "kdcustomhtml"

    {content, syntax, title} = data = @getData()

    # @codeView = new NonEditableAceField defaultValue: Encoder.htmlDecode(content), autoGrow: yes, afterOpen: =>
    #   syntax or= 'javascript'
    #   @codeView.setTheme 'merbivore'
    #   @codeView.setSyntax syntax
    #
    # @codeView.on 'sizes.height.change', ({height}) =>
    #   @$('.wrapper').height height

    hjsSyntax = __aceSettings.aceToHighlightJsSyntaxMap[syntax]

    @codeView = new KDCustomHTMLView
      tagName  : "code"
      pistachio : '{{#(content)}}'
    , data

    @codeView.setClass hjsSyntax if hjsSyntax
    @codeView.unsetClass "kdcustomhtml"

    @syntaxMode = new KDCustomHTMLView
      tagName  : "strong"
      partial  : __aceSettings.syntaxAssociations[syntax][0] or syntax

    @saveButton = new KDButtonView
      title     : ""
      style     : "dark"
      icon      : yes
      iconOnly  : yes
      iconClass : "save"
      callback  : ->
        new KDNotificationView
          title     : "Currently disabled!"
          type      : "mini"
          duration  : 2500

        # CodeSnippetView.emit 'CodeSnippetWantsSave', data

    @openButton = new KDButtonView
      title     : ""
      style     : "dark"
      icon      : yes
      iconOnly  : yes
      iconClass : "open"
      callback  : ->
        fileName      = "localfile:/#{title}"
        file          = FSHelper.createFileFromPath fileName
        file.contents = Encoder.htmlDecode(content)
        file.syntax   = syntax
        appManager.openFileWithApplication file, 'Ace'

    @copyButton = new KDButtonView
      title     : ""
      style     : "dark"
      icon      : yes
      iconOnly  : yes
      iconClass : "select-all"
      callback  : =>
        @utils.selectText @codeView.$()[0]

  render:->

    super()
    @codeView.setData @getData()
    @codeView.render()
    @applySyntaxColoring()

  applySyntaxColoring:( syntax = @getData().syntax)->

    snipView  = @
    hjsSyntax = __aceSettings.aceToHighlightJsSyntaxMap[syntax]

    if hjsSyntax
      requirejs (['js/highlightjs/highlight.js']), ->
        requirejs (["highlightjs/languages/#{hjsSyntax}"]), ->
          try
            hljs.compileModes()
            hljs.highlightBlock snipView.codeView.$()[0],'  '
          catch err
            console.warn "Error applying highlightjs syntax #{syntax}:", err

  viewAppended: ->

    @setTemplate @pistachio()
    @template.update()
    @applySyntaxColoring()

    twOptions = (title) ->
      title : title, placement : "above", offset : 3, delayIn : 300, html : yes, animate : yes

    @saveButton.$().twipsy twOptions("Save")
    @copyButton.$().twipsy twOptions("Select all")
    @openButton.$().twipsy twOptions("Open")

  pistachio:->
    """
    <div class='kdview'>
      {pre{> @codeView}}
      <div class='button-bar'>{{> @saveButton}}{{> @openButton}}{{> @copyButton}}</div>
    </div>
    {{> @syntaxMode}}
    """
