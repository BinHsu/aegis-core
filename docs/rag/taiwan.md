<!--
Bundled example RAG corpus for the Aegis Core Phase 3 demo.

Source        : https://zh.wikipedia.org/zh-tw/臺灣 (lead section)
Fetched       : 2026-04-15 via Wikipedia REST API
                (action=query&prop=extracts&exintro&explaintext&variant=zh-tw)
License       : CC BY-SA 4.0
                https://creativecommons.org/licenses/by-sa/4.0/
Attribution   : Wikipedia contributors, "臺灣," Wikipedia, The Free
                Encyclopedia, last edited 2026-04-13 (lead section).
Modifications : minor — section headers added for chunking and human
                readability; no source paragraphs altered.

This file IS the corpus per ADR-0019 §Decision.5. The vector index
built from it lives at .rag-index/ (gitignored). The seed mechanism
is unified into the engine per ADR-0020 (forthcoming): `engine
--seed --corpus docs/rag/taiwan.md --target=local|cloud` produces
the index using the same bge-m3 GGUF the query path uses — one
embedder, one vector space.

This is one bundled example. `engine --seed` accepts any UTF-8
markdown file with paragraph-level structure, so swapping in your
own corpus is a matter of pointing `--corpus` at a different path.
The Phase 3 demo persona is a foreign tourist preparing a trip to
Taiwan, asking in their own language; this content set is sized for
that scenario and to keep the example commit reviewable.
-->

# 臺灣 — bundled example RAG corpus

## 概覽

臺灣（俗字寫作台灣），西方國家在歷史上亦稱福爾摩沙（葡萄牙語：Formosa），是位於東亞、太平洋西北側的島嶼，地處琉球群島與菲律賓群島之間，西隔臺灣海峽與亞洲大陸相望，海峽最窄距離約130公里，周圍海域以順時鐘方向分別為太平洋（菲律賓海）、巴士海峽（呂宋海峽）、南海、臺灣海峽、東海。目前為中華民國有效統治領土的主要部分。

## 地理與氣候

臺灣島的總陸地面積為35,887平方公里（13,856平方英里），略大於比利時，海岸線長度為1,150.95公里（715.17英里），在當前全球各島嶼面積排名中位居第38（或39），為板塊碰撞隆起（由菲律賓板塊潛入歐亞板塊）形成的大陸島，係東亞島弧之一部分。氣候方面，北回歸線貫穿全島，氣候炎熱夏季偏長，介於熱帶與亞熱帶地帶之間，北回歸線以北為副熱帶季風氣候、以南為熱帶季風氣候，下雨量約近世界平均值之3倍。臺灣地形崎嶇不平，平原主要集中於西部沿海，其餘約七成面積為山地與丘陵，由於地殼變動，海拔變化大,最高點玉山達3,952公尺，以上因素使得人類難以開發台灣，但也讓山區自然景觀與生態系資源豐富多元。南方與菲律賓的呂宋島隔著約250公里（155英里）寬的巴士海峽，西南方為南海，北方為東海，東方則面向菲律賓海。

## 人口與族群

當前統治臺灣的中華民國人口約2,300萬人，超過七成集中於西部的五大都會區，其中以行政中心臺北為核心的臺北都會區最大，約700萬人。族群構成以漢人、原住民族為主：原住民族由多個屬於南島民族的部族組成，漢人則依民系及移民年代的不同而分為閩南（河洛）、客家與外省族群，其中閩南裔為臺灣最大族群。約三萬年前的冰河時期，開始有人類遷移至台灣活動，這些遷徙者逐漸形成原住民族，成為最早世居於臺灣的民族。原住民族在17世紀中葉以前一直是臺灣的多數民族，但隨著漢人不斷從亞洲大陸移入與墾殖，漢人遂取代原住民族在臺灣的多數民族地位。

## 歷史與政治

自有信史記錄以來，臺灣歷史上曾經歷多個原住民聯盟和政權、荷西時期、明鄭時期、清治時期、日治時期等多次政權遞嬗，最近一次為1945年進入戰後時期由中華民國統治。1949年中華民國政府遷移到台灣，使臺灣成為中華民國的主要領土；由此原因，再加上現今世界多數國家認同中華人民共和國為中國的代表，使得「臺灣」成為現今中華民國的通稱。隨著1987年戒嚴時代結束，臺灣逐漸淡化過往戒嚴時代形塑的中國史觀，政治上走向自由化與民主化，以中國國民黨及民主進步黨兩黨為首的政黨政治、統獨議題、以及公民社會的形成，加之以東南亞新住民的定居，產生出多元文化主義，使得臺灣文化呈現多元並立的面貌。台灣以移民為主的人文結構，亦帶來多元的思想觀點。自大航海時代以來，台灣文化就在明鄭、清朝的統治與西方列強的衝擊中經歷多次大變動，並在近代產生「臺灣主體意識」思想。

## 經濟發展

歷經1860年臺灣開港以來至日治時期所打下的現代化基礎，以及中華民國政府遷臺後運用美援所進行的一系列的經濟建設，加上國際上冷戰對峙的格局，臺灣自1960年代起在經濟與社會發展上突飛猛進，締造「臺灣奇蹟」，名列亞洲四小龍之一；之後在1990年代躋身已開發國家之列，目前無論人均所得或人類發展指數均具世界先進國家水準。貿易方面主要透過高科技產業賺取外匯，經濟發展上以高科技產業與服務業為中心，亦朝向文化產業及觀光業發展。
