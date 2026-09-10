#!/usr/bin/env python3
"""高级特性 xlsx 语料生成器(DOC-1 / D6-3 判定用;E-D 语料 harness 的补充生产者)。

**为什么需要它**:`scripts/gen-doc-corpus.sh` 走本机第三方生产者(textutil/cupsfilter/poppler),
但**本机没有 xlsx 生产者**(无 openpyxl/xlsxwriter/LibreOffice;WPS 无脚本接口),
而 D6-3 的触发条件是「真实样本出现 content 级缺口(图表/透视/批注/内嵌图整体丢失)」。
本脚本用**纯标准库按 ECMA-376 手工构造**含高级特性的工作簿,补上这块语料。

**性质与边界(不得含糊)**:这些文件是**合成容器**,不是第三方生产者输出 ——
它们能忠实驱动 `probeGaps()` 的**特性探测**(按部件名/内容正则)与 gah 的抽取路径,
但**不保证 Excel/WPS 打开无修复提示**(透视表/图表仅构造到探测与解析所需的最小结构)。
结论据此只用于「是否存在内容级缺口」,不作为「真实用户文件的特性分布」依据。

生成(默认 3 件):
  advanced-all.xlsx       图表 + 透视表 + 单元格批注(legacy)+ 内嵌图片 + 文本框 +
                          表格对象/自动筛选/条件格式/数据验证/合并/隐藏行/公式/富文本
  threaded-comments.xlsx  现代「回复式批注」(threadedComments + persons)
  pivot-values-only.xlsx  透视结果**只**存在于透视表所在工作表的单元格(检验数据是否真的不可见)

用法: python3 scripts/gen-xlsx-advanced.py [输出目录]
"""
import os
import struct
import sys
import zipfile
import zlib

NS = {
    "ct": "http://schemas.openxmlformats.org/package/2006/content-types",
    "rel": "http://schemas.openxmlformats.org/package/2006/relationships",
    "ss": "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
    "or": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
    "dr": "http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing",
    "a": "http://schemas.openxmlformats.org/drawingml/2006/main",
    "c": "http://schemas.openxmlformats.org/drawingml/2006/chart",
    "tc": "http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments",
    "xdr": "http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing",
}


def png_1x1() -> bytes:
    """最小合法 PNG(1×1 红色)用于内嵌图片特性。"""
    def chunk(tag, data):
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)

    ihdr = struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0)
    raw = b"\x00\xff\x00\x00"
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


def worksheet(rows_xml: str, extra: str = "") -> str:
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<worksheet xmlns="{NS["ss"]}" xmlns:r="{NS["or"]}">'
            f'{extra}<sheetData>{rows_xml}</sheetData></worksheet>')


def shared_strings(items) -> str:
    parts = []
    for it in items:
        if isinstance(it, tuple):  # 富文本
            runs = "".join(f'<r><rPr><b val="{1 if b else 0}"/></rPr><t>{t}</t></r>' for t, b in it)
            parts.append(f"<si>{runs}</si>")
        else:
            parts.append(f"<si><t>{it}</t></si>")
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<sst xmlns="{NS["ss"]}" count="{len(parts)}" uniqueCount="{len(parts)}">'
            + "".join(parts) + "</sst>")


STYLES = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="{NS["ss"]}">
<numFmts count="2"><numFmt numFmtId="164" formatCode="yyyy\\-mm\\-dd"/><numFmt numFmtId="165" formatCode="0.00%"/></numFmts>
<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FFDCE6F1"/></patternFill></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="4">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>
<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
</cellXfs>
<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>'''


def workbook(sheet_names, extra_workbook="") -> str:
    sheets = "".join(
        f'<sheet name="{n}" sheetId="{i+1}" r:id="rId{i+1}"/>' for i, n in enumerate(sheet_names))
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<workbook xmlns="{NS["ss"]}" xmlns:r="{NS["or"]}">'
            f'<sheets>{sheets}</sheets>{extra_workbook}</workbook>')


def workbook_rels(sheet_names, extra=()) -> str:
    rels = "".join(
        f'<Relationship Id="rId{i+1}" Type="{NS["or"]}/worksheet" Target="worksheets/sheet{i+1}.xml"/>'
        for i in range(len(sheet_names)))
    for rid, typ, target in extra:
        rels += f'<Relationship Id="{rid}" Type="{typ}" Target="{target}"/>'
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<Relationships xmlns="{NS["rel"]}">{rels}</Relationships>')


def sheet_rels(rels) -> str:
    body = "".join(f'<Relationship Id="{rid}" Type="{typ}" Target="{tgt}"/>' for rid, typ, tgt in rels)
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<Relationships xmlns="{NS["rel"]}">{body}</Relationships>')


CHART = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<c:chartSpace xmlns:c="{NS["c"]}" xmlns:a="{NS["a"]}" xmlns:r="{NS["or"]}">
<c:chart><c:plotArea><c:layout/>
<c:barChart><c:barDir val="col"/><c:grouping val="clustered"/><c:varyColors val="0"/>
<c:ser><c:idx val="0"/><c:order val="0"/>
<c:tx><c:strRef><c:f>Data!$B$1</c:f><c:strCache><c:ptCount val="1"/><c:pt idx="0"><c:v>金额</c:v></c:pt></c:strCache></c:strRef></c:tx>
<c:val><c:numRef><c:f>Data!$B$2:$B$5</c:f><c:numCache><c:formatCode>General</c:formatCode><c:ptCount val="4"/>
<c:pt idx="0"><c:v>1200</c:v></c:pt><c:pt idx="1"><c:v>1850</c:v></c:pt><c:pt idx="2"><c:v>1430</c:v></c:pt><c:pt idx="3"><c:v>2110</c:v></c:pt></c:numCache></c:numRef></c:val>
</c:ser>
<c:axId val="111111111"/><c:axId val="222222222"/></c:barChart>
<c:catAx><c:axId val="111111111"/><c:scaling><c:orientation val="minMax"/></c:scaling><c:delete val="0"/><c:axPos val="b"/><c:crossAx val="222222222"/></c:catAx>
<c:valAx><c:axId val="222222222"/><c:scaling><c:orientation val="minMax"/></c:scaling><c:delete val="0"/><c:axPos val="l"/><c:crossAx val="111111111"/></c:valAx>
</c:plotArea><c:plotVisOnly val="1"/><c:dispBlanksAs val="gap"/></c:chart></c:chartSpace>'''


def drawing_with_pic_and_textbox() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<xdr:wsDr xmlns:xdr="{NS["xdr"]}" xmlns:a="{NS["a"]}" xmlns:r="{NS["or"]}">
<xdr:twoCellAnchor>
<xdr:from><xdr:col>4</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>1</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from>
<xdr:to><xdr:col>7</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>8</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to>
<xdr:pic><xdr:nvPicPr><xdr:cNvPr id="2" name="Picture 1"/><xdr:cNvPicPr/></xdr:nvPicPr>
<xdr:blipFill><a:blip r:embed="rId2"/><a:stretch><a:fillRect/></a:stretch></xdr:blipFill>
<xdr:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="914400" cy="914400"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></xdr:spPr></xdr:pic>
<xdr:clientData/></xdr:twoCellAnchor>
<xdr:twoCellAnchor>
<xdr:from><xdr:col>1</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>8</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from>
<xdr:to><xdr:col>4</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>12</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to>
<xdr:sp><xdr:nvSpPr><xdr:cNvPr id="3" name="TextBox 1"/><xdr:cNvSpPr txBox="1"/></xdr:nvSpPr>
<xdr:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1828800" cy="914400"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></xdr:spPr>
<xdr:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>文本框里的说明文字(仅存在于 drawing 中)</a:t></a:r></a:p></xdr:txBody>
</xdr:sp><xdr:clientData/></xdr:twoCellAnchor>
</xdr:wsDr>'''


def drawing_rels() -> str:
    return sheet_rels([
        ("rId1", f'{NS["or"]}/chart', "../charts/chart1.xml"),
        ("rId2", f'{NS["or"]}/image', "../media/image1.png"),
    ])


def pivot_cache() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotCacheDefinition xmlns="{NS["ss"]}" xmlns:r="{NS["or"]}" r:id="rId1" refreshOnLoad="1" recordCount="4">
<cacheSource type="worksheet"><worksheetSource ref="A1:B5" sheet="Data"/></cacheSource>
<cacheFields count="2">
<cacheField name="产品" numFmtId="0"><sharedItems count="2"><s v="A 型"/><s v="B 型"/></sharedItems></cacheField>
<cacheField name="金额" numFmtId="0"><sharedItems count="4"><n v="1200"/><n v="1850"/><n v="1430"/><n v="2110"/></sharedItems></cacheField>
</cacheFields></pivotCacheDefinition>'''


def pivot_cache_rels() -> str:
    return sheet_rels([("rId1", f'{NS["or"]}/worksheet', "../worksheets/sheet1.xml")])


def pivot_table() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<pivotTableDefinition xmlns="{NS["ss"]}" name="PivotTable1" cacheId="1" dataOnRows="1" applyNumberFormats="0"
 applyBorderFormats="0" applyFontFormats="0" applyPatternFormats="0" applyAlignmentFormats="0" applyWidthHeightFormats="1">
<location ref="A2:B5" firstHeaderRow="1" firstDataRow="1" firstDataCol="1"/>
<pivotFields count="2">
<pivotField axis="axisRow" showAll="0"><items count="1"><item t="default"/></items></pivotField>
<pivotField dataField="1" showAll="0"><items count="1"><item t="default"/></items></pivotField>
</pivotFields>
<rowFields count="1"><field x="0"/></rowFields>
<rowItems count="1"><i><x/></i></rowItems>
<colItems count="1"><i/></colItems>
<dataFields count="1"><dataField name="求和项:金额" fld="1" baseField="0" baseItem="0"/></dataFields>
</pivotTableDefinition>'''


def comments_legacy() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<comments xmlns="{NS["ss"]}"><authors><author>审阅人 张三</author></authors>
<commentList><comment ref="B3" authorId="0"><text><r><rPr><b/><sz val="9"/></rPr><t>这个数字与上季度口径不一致,请复核后再发布。</t></r><r><rPr><sz val="9"/></rPr><t xml:space="preserve"> —— 2026-10-11</t></r></text></comment></commentList>
</comments>'''


def threaded_comments() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<ThreadedComments xmlns="{NS["tc"]}">
<threadedComment ref="B2" personId="{{11111111-1111-1111-1111-111111111111}}" id="{{aaaaaaaa-1111-1111-1111-111111111111}}" date="2026-10-11T02:00:00Z">
<text>@李四 这里的口径要跟合同附件对齐。</text></threadedComment>
<threadedComment ref="B2" personId="{{22222222-2222-2222-2222-222222222222}}" id="{{bbbbbbbb-2222-2222-2222-222222222222}}" date="2026-10-11T03:00:00Z" parentId="{{aaaaaaaa-1111-1111-1111-111111111111}}">
<text>已按合同第 3.2 条修正,请复核。</text></threadedComment>
</ThreadedComments>'''


def persons() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<personList xmlns="{NS["tc"]}">
<person displayName="张三" id="{{11111111-1111-1111-1111-111111111111}}" userId="zhangsan@example.com" providerId="None"/>
<person displayName="李四" id="{{22222222-2222-2222-2222-222222222222}}" userId="lisi@example.com" providerId="None"/>
</personList>'''


def content_types(parts) -> str:
    defaults = ('<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
                '<Default Extension="xml" ContentType="application/xml"/>'
                '<Default Extension="png" ContentType="image/png"/>'
                '<Default Extension="vml" ContentType="application/vnd.openxmlformats-officedocument.vmlDrawing"/>')
    overrides = "".join(f'<Override PartName="/{p}" ContentType="{ct}"/>' for p, ct in parts)
    return (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            f'<Types xmlns="{NS["ct"]}">{defaults}{overrides}</Types>')


ROOT_RELS = (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
             f'<Relationships xmlns="{NS["rel"]}">'
             f'<Relationship Id="rId1" Type="{NS["or"]}/officeDocument" Target="xl/workbook.xml"/>'
             f'</Relationships>')


def write_xlsx(path, entries):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for name, body in entries.items():
            if isinstance(body, str):
                body = body.encode("utf-8")
            z.writestr(name, body)


def build_advanced_all(path):
    shared = shared_strings([
        (("季度销售汇总", True), (" (标题)", False)),  # 0 富文本(多 run)
        "产品", "金额", "A 型", "B 型", "合计(隐藏行)", "公式合计", "统计日期", "占比",
    ])
    # 行内共享串下标:标题用富文本 0
    sheet1_extra = (
        '<sheetPr/><dimension ref="A1:B10"/>'
        '<sheetViews><sheetView workbookViewId="0"/></sheetViews>'
        '<mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells>'
        '<autoFilter ref="A2:B6"/>'
        '<conditionalFormatting sqref="B3:B6"><cfRule type="cellIs" dxfId="0" priority="1" operator="greaterThan"><formula>1500</formula></cfRule></conditionalFormatting>'
        '<dataValidation type="whole" operator="between" sqref="B3:B6"><formula1>0</formula1><formula2>100000</formula2></dataValidation>'
        '<drawing r:id="rIdDraw"/>'
    )
    sheet1_rows = "".join([
        '<row r="1" ht="20" customHeight="1"><c r="A1" s="1" t="s"><v>0</v></c><c r="B1" s="1"/></row>',
        '<row r="2"><c r="A2" s="1" t="s"><v>1</v></c><c r="B2" s="1" t="s"><v>2</v></c></row>',
        '<row r="3"><c r="A3" t="s"><v>3</v></c><c r="B3"><v>1200</v></c></row>',
        '<row r="4"><c r="A4" t="s"><v>4</v></c><c r="B4"><v>1850</v></c></row>',
        '<row r="5"><c r="A5" t="s"><v>3</v></c><c r="B5"><v>1430</v></c></row>',
        '<row r="6"><c r="A6" t="s"><v>4</v></c><c r="B6"><v>2110</v></c></row>',
        '<row r="7" hidden="1"><c r="A7" t="s"><v>5</v></c><c r="B7"><v>6590</v></c></row>',
        '<row r="8"><c r="A8" t="s"><v>6</v></c><c r="B8"><f>SUM(B3:B6)</f><v>6590</v></c></row>',
        '<row r="9"><c r="A9" t="s"><v>7</v></c><c r="B9" s="2"><v>45147</v></c></row>',
        '<row r="10"><c r="A10" t="s"><v>8</v></c><c r="B10" s="3"><v>0.42</v></c></row>',
    ])
    sheet2_rows = "".join([
        '<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>',
        '<row r="2"><c r="A2" t="inlineStr"><is><t>合计</t></is></c><c r="B2"><v>6590</v></c></row>',
    ])
    entries = {
        "[Content_Types].xml": content_types([
            ("xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"),
            ("xl/worksheets/sheet1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/worksheets/sheet2.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/worksheets/sheet3.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/styles.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"),
            ("xl/sharedStrings.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"),
            ("xl/charts/chart1.xml", "application/vnd.openxmlformats-officedocument.drawingml.chart+xml"),
            ("xl/drawings/drawing1.xml", "application/vnd.openxmlformats-officedocument.drawing+xml"),
            ("xl/pivotCache/pivotCacheDefinition1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"),
            ("xl/pivotTables/pivotTable1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"),
            ("xl/comments1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.comments+xml"),
            ("xl/tables/table1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.table+xml"),
        ]),
        "_rels/.rels": ROOT_RELS,
        "xl/workbook.xml": workbook(["Data", "Pivot", "Chart"]),
        "xl/_rels/workbook.xml.rels": workbook_rels(["Data", "Pivot", "Chart"], extra=[
            ("rId10", f'{NS["or"]}/sharedStrings', "sharedStrings.xml"),
            ("rId11", f'{NS["or"]}/styles', "styles.xml"),
        ]),
        "xl/sharedStrings.xml": shared,
        "xl/styles.xml": STYLES,
        "xl/worksheets/sheet1.xml": worksheet(sheet1_rows, sheet1_extra),
        "xl/worksheets/sheet2.xml": worksheet(sheet2_rows),
        "xl/worksheets/sheet3.xml": worksheet(
            '<row r="1"><c r="A1" t="inlineStr"><is><t>图表数据源见 Data 表(图表本身不导出)</t></is></c></row>'),
        # Data 表关联:drawing + 批注 + 表格对象 + 透视表(透视表挂在 Pivot 表)
        "xl/worksheets/_rels/sheet1.xml.rels": sheet_rels([
            ("rIdDraw", f'{NS["or"]}/drawing', "../drawings/drawing1.xml"),
            ("rIdCom", f'{NS["or"]}/comments', "../comments1.xml"),
            ("rIdTab", f'{NS["or"]}/table', "../tables/table1.xml"),
        ]),
        "xl/worksheets/_rels/sheet2.xml.rels": sheet_rels([
            ("rIdPivot", f'{NS["or"]}/pivotTable', "../pivotTables/pivotTable1.xml"),
            ("rIdChart", f'{NS["or"]}/drawing', "../drawings/drawing2.xml"),
        ]),
        "xl/drawings/drawing1.xml": drawing_with_pic_and_textbox(),
        "xl/drawings/drawing2.xml": drawing_chart_only(),
        "xl/drawings/_rels/drawing1.xml.rels": drawing_rels(),
        "xl/drawings/_rels/drawing2.xml.rels": sheet_rels([
            ("rId1", f'{NS["or"]}/chart', "../charts/chart1.xml"),
        ]),
        "xl/charts/chart1.xml": CHART,
        "xl/media/image1.png": png_1x1(),
        "xl/pivotCache/pivotCacheDefinition1.xml": pivot_cache(),
        "xl/pivotCache/_rels/pivotCacheDefinition1.xml.rels": pivot_cache_rels(),
        "xl/pivotTables/pivotTable1.xml": pivot_table(),
        "xl/pivotTables/_rels/pivotTable1.xml.rels": sheet_rels([
            ("rId1", f'{NS["or"]}/pivotCacheDefinition', "../pivotCache/pivotCacheDefinition1.xml"),
        ]),
        "xl/comments1.xml": comments_legacy(),
        "xl/tables/table1.xml": TABLE,
    }
    write_xlsx(path, entries)


def drawing_chart_only() -> str:
    return f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<xdr:wsDr xmlns:xdr="{NS["xdr"]}" xmlns:a="{NS["a"]}" xmlns:c="{NS["c"]}" xmlns:r="{NS["or"]}">
<xdr:twoCellAnchor>
<xdr:from><xdr:col>3</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>2</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from>
<xdr:to><xdr:col>10</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>18</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to>
<xdr:graphicFrame macro=""><xdr:nvGraphicFramePr>
<xdr:cNvPr id="2" name="Chart 1"/><xdr:cNvGraphicFramePr/></xdr:nvGraphicFramePr>
<xdr:xfrm><a:off x="0" y="0"/><a:ext cx="5486400" cy="3200400"/></xdr:xfrm>
<a:graphic><a:graphicData uri="{NS["c"]}"><c:chart r:id="rId1"/></a:graphicData></a:graphic>
</xdr:graphicFrame><xdr:clientData/></xdr:twoCellAnchor>
</xdr:wsDr>'''


TABLE = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<table xmlns="{NS["ss"]}" id="1" name="Table1" displayName="Table1" ref="A2:B6" headerRowCount="1" totalsRowShown="0">
<autoFilter ref="A2:B6"/>
<tableColumns count="2"><tableColumn id="1" name="产品"/><tableColumn id="2" name="金额"/></tableColumns>
<tableStyleInfo name="TableStyleMedium2" showFirstColumn="0" showLastColumn="0" showRowStripes="1" showColumnStripes="0"/>
</table>'''


def build_threaded_comments(path):
    sheet1_rows = ('<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>'
                   '<row r="2"><c r="A2" t="inlineStr"><is><t>合同金额</t></is></c><c r="B2"><v>88000</v></c></row>'
                   '<row r="3"><c r="A3" t="inlineStr"><is><t>附件编号</t></is></c><c r="B3" t="inlineStr"><is><t>ATT-2026-1023</t></is></c></row>')
    entries = {
        "[Content_Types].xml": content_types([
            ("xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"),
            ("xl/worksheets/sheet1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/sharedStrings.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"),
            ("xl/threadedComments/threadedComment1.xml", "application/vnd.ms-excel.threadedcomments+xml"),
            ("xl/persons/person.xml", "application/vnd.ms-excel.person+xml"),
        ]),
        "_rels/.rels": ROOT_RELS,
        "xl/workbook.xml": workbook(["合同"], extra_workbook='<extLst/>'),
        "xl/_rels/workbook.xml.rels": workbook_rels(["合同"], extra=[
            ("rId10", f'{NS["or"]}/sharedStrings', "sharedStrings.xml"),
            ("rId20", "http://schemas.microsoft.com/office/2017/10/relationships/person", "persons/person.xml"),
        ]),
        "xl/sharedStrings.xml": shared_strings(["科目", "数值"]),
        "xl/worksheets/sheet1.xml": worksheet(sheet1_rows),
        "xl/worksheets/_rels/sheet1.xml.rels": sheet_rels([
            ("rIdTC", "http://schemas.microsoft.com/office/2017/10/relationships/threadedComment",
             "../threadedComments/threadedComment1.xml"),
        ]),
        "xl/threadedComments/threadedComment1.xml": threaded_comments(),
        "xl/persons/person.xml": persons(),
    }
    write_xlsx(path, entries)


def build_pivot_values_only(path):
    # 透视结果只写在 Pivot 表单元格里(源数据在另一表);用于检验「数据是否真的不可见」
    sheet1 = ('<row r="1"><c r="A1" t="inlineStr"><is><t>区域</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row>'
              '<row r="2"><c r="A2" t="inlineStr"><is><t>华东</t></is></c><c r="B2"><v>3200</v></c></row>'
              '<row r="3"><c r="A3" t="inlineStr"><is><t>华北</t></is></c><c r="B3"><v>2100</v></c></row>')
    # 透视表输出:含 Excel 写入的计算结果单元格
    sheet2 = ('<row r="1"><c r="A1" t="inlineStr"><is><t>求和项:金额</t></is></c></row>'
              '<row r="2"><c r="A2" t="inlineStr"><is><t>华东</t></is></c><c r="B2"><v>3200</v></c></row>'
              '<row r="3"><c r="A3" t="inlineStr"><is><t>华北</t></is></c><c r="B3"><v>2100</v></c></row>'
              '<row r="4"><c r="A4" t="inlineStr"><is><t>总计</t></is></c><c r="B4"><v>5300</v></c></row>')
    entries = {
        "[Content_Types].xml": content_types([
            ("xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"),
            ("xl/worksheets/sheet1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/worksheets/sheet2.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"),
            ("xl/pivotCache/pivotCacheDefinition1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"),
            ("xl/pivotTables/pivotTable1.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"),
        ]),
        "_rels/.rels": ROOT_RELS,
        "xl/workbook.xml": workbook(["源数据", "透视结果"]),
        "xl/_rels/workbook.xml.rels": workbook_rels(["源数据", "透视结果"], extra=[
            ("rId10", f'{NS["or"]}/pivotCacheDefinition', "pivotCache/pivotCacheDefinition1.xml"),
        ]),
        "xl/pivotCache/pivotCacheDefinition1.xml": pivot_cache().replace('sheet="Data"', 'sheet="源数据"'),
        "xl/pivotCache/_rels/pivotCacheDefinition1.xml.rels": sheet_rels([
            ("rId1", f'{NS["or"]}/worksheet', "../worksheets/sheet1.xml"),
        ]),
        "xl/pivotTables/pivotTable1.xml": pivot_table(),
        "xl/pivotTables/_rels/pivotTable1.xml.rels": sheet_rels([
            ("rId1", f'{NS["or"]}/pivotCacheDefinition', "../pivotCache/pivotCacheDefinition1.xml"),
        ]),
        "xl/worksheets/sheet1.xml": worksheet(sheet1),
        "xl/worksheets/sheet2.xml": worksheet(sheet2),
        "xl/worksheets/_rels/sheet2.xml.rels": sheet_rels([
            ("rIdPivot", f'{NS["or"]}/pivotTable', "../pivotTables/pivotTable1.xml"),
        ]),
    }
    write_xlsx(path, entries)


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else os.getcwd()
    os.makedirs(out, exist_ok=True)
    jobs = [
        ("advanced-all.xlsx", build_advanced_all),
        ("threaded-comments.xlsx", build_threaded_comments),
        ("pivot-values-only.xlsx", build_pivot_values_only),
    ]
    for name, fn in jobs:
        path = os.path.join(out, name)
        fn(path)
        print(f"  生成: {path} ({os.path.getsize(path)} 字节)")


if __name__ == "__main__":
    main()
