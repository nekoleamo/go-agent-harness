#!/usr/bin/env python3
"""高级特性 docx 语料生成器(DOC-3a 判定用;E-D 语料 harness 的补充生产者)。

**为什么需要它**:`scripts/gen-doc-corpus.sh` 的 docx 来自 macOS `textutil`,它**无法写批注**
(而批注/回复式批注/文本框正是 docx 侧此前丢失的三类内容)。本脚本用**纯标准库按 ECMA-376
手工构造**含批注(含回复线程)、people 显示名、文本框与表格的文档,补上这块语料。

**性质与边界(不得含糊)**:合成容器,不是第三方生产者输出 —— 足以驱动 `probeGaps()` 的
特性探测与 gah 的抽取路径,但不保证 Word 打开无修复提示。结论只用于「是否仍丢文本」。

用法: python3 scripts/gen-docx-advanced.py [输出目录]
"""
import os
import sys
import zipfile

CT = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/comments.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.comments+xml"/>
<Override PartName="/word/commentsExtended.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.commentsExtended+xml"/>
<Override PartName="/word/people.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.people+xml"/>
<Override PartName="/word/charts/chart1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawingml.chart+xml"/>
<Override PartName="/word/diagrams/data1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawingml.diagramData+xml"/>
</Types>"""

ROOT_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>"""

DOC_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rIdC" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="comments.xml"/>
<Relationship Id="rIdChart" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="charts/chart1.xml"/>
<Relationship Id="rIdDiag" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/diagramData" Target="diagrams/data1.xml"/>
</Relationships>"""

STYLES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:style w:type="paragraph" w:styleId="Normal" w:default="1"><w:name w:val="Normal"/></w:style>
</w:styles>"""

# 正文:一段正文 + 段落内文本框(Choice/Fallback 双写,检验去重)+ 表格 + 一段正文
DOCUMENT = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
 xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
 xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"
 xmlns:v="urn:schemas-microsoft-com:vml">
<w:body>
<w:p><w:pPr><w:pStyle w:val="Normal"/></w:pPr>
 <w:r><w:t>采购合同审阅要点(语料样本)</w:t></w:r>
 <w:r><mc:AlternateContent>
  <mc:Choice Requires="wps"><w:drawing><wp:inline><a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData>
   <wps:wsp xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"><wps:txbx>
     <w:txbxContent><w:p><w:r><w:t>批注口径以合同附件 3.2 条为准</w:t></w:r></w:p><w:p><w:r><w:t>附件编号 ATT-2026-1023</w:t></w:r></w:p></w:txbxContent>
   </wps:txbx></wps:wsp>
  </a:graphicData></a:graphic></wp:inline></w:drawing></mc:Choice>
  <mc:Fallback><w:pict><v:shape><v:textbox>
     <w:txbxContent><w:p><w:r><w:t>批注口径以合同附件 3.2 条为准</w:t></w:r></w:p><w:p><w:r><w:t>附件编号 ATT-2026-1023</w:t></w:r></w:p></w:txbxContent>
  </v:textbox></v:shape></w:pict></mc:Fallback>
 </mc:AlternateContent></w:r>
</w:p>
<w:tbl>
 <w:tr><w:tc><w:p><w:r><w:t>条款</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>金额</w:t></w:r></w:p></w:tc></w:tr>
 <w:tr><w:tc><w:p><w:r><w:t>3.2 付款条件</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>88000</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>以上内容请法务复核后签署。</w:t></w:r></w:p>
</w:body></w:document>"""

# 批注:id=1 根 + id=2 回复(paraId 线程关系)+ id=3 独立
COMMENTS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
 xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml">
<w:comment w:id="1" w:author="张三" w:date="2026-10-11T02:00:00Z" w14:paraId="11111111">
 <w:p w14:paraId="11111111"><w:r><w:t>付款条件与上一版不一致,请复核</w:t></w:r></w:p>
</w:comment>
<w:comment w:id="2" w:author="李四" w:date="2026-10-11T03:00:00Z" w14:paraId="22222222">
 <w:p w14:paraId="22222222"><w:r><w:t>已按附件 3.2 条修正</w:t></w:r><w:r><w:t>(含滞纳金上限)</w:t></w:r></w:p>
</w:comment>
<w:comment w:id="3" w:author="lisi@example.com" w:date="2026-10-11T04:00:00Z" w14:paraId="33333333">
 <w:p w14:paraId="33333333"><w:r><w:t>独立批注:签署页需盖骑缝章</w:t></w:r></w:p>
</w:comment>
</w:comments>"""

COMMENTS_EX = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w15:commentsEx xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml">
<w15:commentEx w15:paraId="22222222" w15:done="0" w15:parentParaId="11111111"/>
</w15:commentsEx>"""

PEOPLE = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w15:people xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml">
<w15:person w15:author="lisi@example.com" w15:contact="lisi@example.com"><w15:presenceInfo w15:providerId="None" w15:userId="lisi@example.com"/></w15:person>
</w15:people>"""


# 图表(柱状;含缓存数据 —— 类别 strCache + 数值 numCache,数据即内容)
CHART = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>各条款金额分布</a:t></a:r></a:p></c:rich></c:tx></c:title>
<c:plotArea><c:layout/>
<c:barChart><c:barDir val="col"/><c:grouping val="clustered"/>
<c:ser><c:idx val="0"/><c:order val="0"/>
 <c:tx><c:strRef><c:f>Sheet1!$B$1</c:f><c:strCache><c:ptCount val="1"/><c:pt idx="0"><c:v>金额</c:v></c:pt></c:strCache></c:strRef></c:tx>
 <c:cat><c:strRef><c:f>Sheet1!$A$2:$A$5</c:f><c:strCache><c:ptCount val="4"/>
   <c:pt idx="0"><c:v>3.2 付款条件</c:v></c:pt><c:pt idx="1"><c:v>3.3 交付</c:v></c:pt>
   <c:pt idx="2"><c:v>3.4 验收</c:v></c:pt><c:pt idx="3"><c:v>3.5 违约</c:v></c:pt></c:strCache></c:strRef></c:cat>
 <c:val><c:numRef><c:f>Sheet1!$B$2:$B$5</c:f><c:numCache><c:formatCode>General</c:formatCode><c:ptCount val="4"/>
   <c:pt idx="0"><c:v>88000</c:v></c:pt><c:pt idx="1"><c:v>12000</c:v></c:pt>
   <c:pt idx="2"><c:v>4500</c:v></c:pt><c:pt idx="3"><c:v>20000</c:v></c:pt></c:numCache></c:numRef></c:val>
</c:ser>
</c:barChart><c:catAx><c:axId val="1"/><c:crossAx val="2"/></c:catAx><c:valAx><c:axId val="2"/><c:crossAx val="1"/></c:valAx>
</c:plotArea></c:chart></c:chartSpace>"""

# SmartArt 数据模型(文字在 dgm:dataModel 的 a:t;版式不做)
DIAGRAM = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<dgm:dataModel xmlns:dgm="http://schemas.openxmlformats.org/drawingml/2006/diagram" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<dgm:ptLst>
<dgm:pt modelId="1" type="doc"><dgm:prSet/><dgm:t><a:p><a:r><a:t>付款流程</a:t></a:r></a:p></dgm:t></dgm:pt>
<dgm:pt modelId="2"><dgm:prSet/><dgm:t><a:p><a:r><a:t>提交发票</a:t></a:r></a:p></dgm:t></dgm:pt>
<dgm:pt modelId="3"><dgm:prSet/><dgm:t><a:p><a:r><a:t>财务复核</a:t></a:r></a:p></dgm:t></dgm:pt>
<dgm:pt modelId="4"><dgm:prSet/><dgm:t><a:p><a:r><a:t>付款</a:t></a:r></a:p></dgm:t></dgm:pt>
</dgm:ptLst></dgm:dataModel>"""


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else os.getcwd()
    os.makedirs(out, exist_ok=True)
    path = os.path.join(out, "advanced-comments.docx")
    parts = {
        "[Content_Types].xml": CT,
        "_rels/.rels": ROOT_RELS,
        "word/document.xml": DOCUMENT,
        "word/_rels/document.xml.rels": DOC_RELS,
        "word/styles.xml": STYLES,
        "word/comments.xml": COMMENTS,
        "word/commentsExtended.xml": COMMENTS_EX,
        "word/people.xml": PEOPLE,
        "word/charts/chart1.xml": CHART,
        "word/diagrams/data1.xml": DIAGRAM,
    }
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for name, body in parts.items():
            z.writestr(name, body)
    print(f"  生成: {path} ({os.path.getsize(path)} 字节)")


if __name__ == "__main__":
    main()
