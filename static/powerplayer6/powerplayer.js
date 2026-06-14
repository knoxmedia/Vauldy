/**
 * PW Player Source start cap
 * 
 * This will appear at the top of the PW Player source
 * 
 * @version 5.7
 */

 if (typeof powerplayer == "undefined") {/**
 * PW Player namespace definition
 * @version 5.8
 */

function currentScriptUrl() {
    var scripts = document.getElementsByTagName('script')
    // var url = scripts.length ? scripts[scripts.length - 1].src : ''
	var url = ''
	for (var i = 0; i < scripts.length; i = i + 1) {
        var script = scripts[i];
        if (script.src.indexOf("powerplayer.js") == -1 && script.src.indexOf("powerplayer.min.js") == -1) {
            continue;
        }
		url = script.src
	}
	var pos = url.indexOf('?')
	if(pos > 0) {
    	url = url.substring(0, pos)
	}
	return url
}

var baseUrl = currentScriptUrl().replace(/\/[^/]+$/, '')

var powerplayer = function(container) {
	if (powerplayer.api){
		return powerplayer.api.selectPlayer(container);
	}
};

var $pw = powerplayer;

powerplayer.version = '6.0.31';

// "Shiv" method for older IE browsers; required for parsing media tags
powerplayer.vid = document.createElement("video");
powerplayer.audio = document.createElement("audio");
powerplayer.source = document.createElement("source");
powerplayer.baseUrl = baseUrl;/**
 * Utility methods for the PW Player.
 * 
 * @author zach, pablo
 * @version 5.9
 */
(function(powerplayer) {

	powerplayer.utils = function() {
	};

	/** Returns the true type of an object * */

	/**
	 * 
	 * @param {Object}
	 *            value
	 */
	powerplayer.utils.typeOf = function(value) {
		var s = typeof value;
		if (s === 'object') {
			if (value) {
				if (value instanceof Array) {
					s = 'array';
				}
			} else {
				s = 'null';
			}
		}
		return s;
	};

	/** Merges a list of objects * */
	powerplayer.utils.extend = function() {
		var args = powerplayer.utils.extend['arguments'];
		if (args.length > 1) {
			for ( var i = 1; i < args.length; i++) {
				for ( var element in args[i]) {
					args[0][element] = args[i][element];
				}
			}
			return args[0];
		}
		return null;
	};

	/**
	 * Returns a deep copy of an object.
	 * 
	 * @param {Object}
	 *            obj
	 */
	powerplayer.utils.clone = function(obj) {
		var result;
		var args = powerplayer.utils.clone['arguments'];
		if (args.length == 1) {
			switch (powerplayer.utils.typeOf(args[0])) {
			case "object":
				result = {};
				for ( var element in args[0]) {
					result[element] = powerplayer.utils.clone(args[0][element]);
				}
				break;
			case "array":
				result = [];
				for ( var element in args[0]) {
					result[element] = powerplayer.utils.clone(args[0][element]);
				}
				break;
			default:
				return args[0];
				break;
			}
		}
		return result;
	};

	/** Returns the extension of a file name * */
	powerplayer.utils.extension = function(path) {
		if (!path) { return ""; }
		path = path.substring(path.lastIndexOf("/") + 1, path.length);
		path = path.split("?")[0];
		if (path.lastIndexOf('.') > -1) {
			return path.substr(path.lastIndexOf('.') + 1, path.length)
					.toLowerCase();
		}
		return;
	};

	/** Updates the contents of an HTML element * */
	powerplayer.utils.html = function(element, content) {
		element.innerHTML = content;
	};

	/** Wraps an HTML element with another element * */
	powerplayer.utils.wrap = function(originalElement, appendedElement) {
		if (originalElement.parentNode) {
			originalElement.parentNode.replaceChild(appendedElement,
					originalElement);
		}
		appendedElement.appendChild(originalElement);
	};

	/** Loads an XML file into a DOM object * */
	powerplayer.utils.ajax = function(xmldocpath, completecallback, errorcallback, type) {
		var xmlhttp;
		var parsedJSON;
		if (window.XMLHttpRequest) {
			// IE>7, Firefox, Chrome, Opera, Safari
			xmlhttp = new XMLHttpRequest();
		} else {
			// IE6
			xmlhttp = new ActiveXObject("Microsoft.XMLHTTP");
		}
		xmlhttp.onreadystatechange = function() {
			if (xmlhttp.readyState === 4) {
				if (xmlhttp.status === 200) {
					if (completecallback) {
                        // Handle the case where an XML document was returned with an incorrect MIME type.
						if (type === 'xml') {
							try {
								if(!powerplayer.utils.exists(xmlhttp.responseXML)) {
                                    if (window.DOMParser) {
                                        var parsedXML = (new DOMParser()).parseFromString(xmlhttp.responseText,"text/xml");
                                        if (parsedXML) {
                                            xmlhttp = powerplayer.utils.extend({}, xmlhttp, {responseXML:parsedXML});
                                        }
                                    } else {
                                        // Internet Explorer
                                        parsedXML = new ActiveXObject("Microsoft.XMLDOM");
                                        parsedXML.async="false";
                                        parsedXML.loadXML(xmlhttp.responseText);
                                        xmlhttp = powerplayer.utils.extend({}, xmlhttp, {responseXML:parsedXML});
                                    }
								}
                                completecallback(xmlhttp);
							} catch(e) {
								if (errorcallback) {
									errorcallback(xmldocpath);
								}
							}
						} else if(type === 'json') {
                            try {
                                parsedJSON = JSON.parse(xmlhttp.responseText);
                                completecallback(parsedJSON);
                            } catch(e) {
                                if (errorcallback) {
                                    errorcallback(xmldocpath);
                                    return;
                                }
                            }
                        } else {
                            completecallback(xmlhttp);
                            return;
                        }
                        					}
				} else {
					if (errorcallback) {
						errorcallback(xmldocpath);
					}
				}
			}
		};
		try {
			xmlhttp.open("GET", xmldocpath, true);
			xmlhttp.send(null);
		} catch (error) {
			if (errorcallback) {
				errorcallback(xmldocpath);
			}
		}
		return xmlhttp;
	};

	powerplayer.utils.jsonp = function (url, callback) {
        // use setTimeout to handle multiple requests problem, force them in
        // a queue
        setTimeout(function() {
                var head = document.getElementsByTagName("head")[0] || document.documentElement;
                var script = document.createElement("script");
                // add the param callback to url, and avoid cache
                script.src = url + "&callback=" + callback + "&r=" + Math.random() * 10000000;
                // Use insertBefore instead of appendChild to circumvent an IE6
                // bug.
                // This arises when a base node is used.
                head.insertBefore(script, head.firstChild);
                script.onload = script.onreadystatechange = function() {
                    // use /loaded|complete/.test( script.readyState ) to test
                    // IE6 ready,!this.readyState to test FF
                    if (!this.readyState || /loaded|complete/.test(script.readyState)) {
                        // Handle memory leak in IE
                        script.onload = script.onreadystatechange = null;
                        if (head && script.parentNode) {
                            head.removeChild(script);
                        }
                    }
                };
            },
            0);
    }
	/** Loads a file * */
	powerplayer.utils.load = function(domelement, completecallback, errorcallback) {
		domelement.onreadystatechange = function() {
			if (domelement.readyState === 4) {
				if (domelement.status === 200) {
					if (completecallback) {
						completecallback();
					}
				} else {
					if (errorcallback) {
						errorcallback();
					}
				}
			}
		};
	};

	/** Finds tags in a DOM, returning a new DOM * */
	powerplayer.utils.find = function(dom, tag) {
		return dom.getElementsByTagName(tag);
	};

	/** * */

	/** Appends an HTML element to another element HTML element * */
	powerplayer.utils.append = function(originalElement, appendedElement) {
		originalElement.appendChild(appendedElement);
	};

    powerplayer.utils.userAgentMatch = function(regex) {
        var agent = navigator.userAgent.toLowerCase();
        return (agent.match(regex) !== null);
    };
	/**
	 * Detects whether the current browser is IE !+"\v1" technique from
	 * http://webreflection.blogspot.com/2009/01/32-bytes-to-know-if-your-browser-is-ie.html
	 * Note - this detection no longer works for IE9, hence the detection for
	 * window.ActiveXObject
	 */
    powerplayer.utils.isEdge = function() {
        return powerplayer.utils.userAgentMatch(/\sEdge\/\d+/i);
    };

    powerplayer.utils.isIETrident = function(version) {
        if (version) {
            version = parseFloat(version).toFixed(1);
            return powerplayer.utils.userAgentMatch(new RegExp('trident/.+rv:\\s*' + version, 'i'));
        }
        return powerplayer.utils.userAgentMatch(/trident/i);
    };

    powerplayer.utils.isMSIE = function(version) {
        if (version) {
            version = parseFloat(version).toFixed(1);
            return powerplayer.utils.userAgentMatch(new RegExp('msie\\s*' + version, 'i'));
        }
        return powerplayer.utils.userAgentMatch(/msie/i);
    };

    powerplayer.utils.isIE = function(version) {
        if (version) {
            version = parseFloat(version).toFixed(1);
            if (version >= 11) {
                return powerplayer.utils.isIETrident(version);
            } else {
                return powerplayer.utils.isMSIE(version);
            }
        }
        return powerplayer.utils.isEdge() || powerplayer.utils.isMSIE() || powerplayer.utils.isIETrident();
    };

    powerplayer.utils.getIEVersion = function() {
        var ua = navigator.userAgent;
        var ver = 0;
        if(ua){
            if(ua.match(/MSIE\s+([\d]+)\./i)){
                ver = RegExp.$1;
            }else if(ua.match(/Trident.*rv\s*:\s*([\d]+)\./i)){
                ver = RegExp.$1;
            }
        }
        return parseInt(ver);
	}

	// powerplayer.utils.isIE = function() {
	// 	return ((!+"\v1") || (typeof window.ActiveXObject != "undefined"));
	// };
	
	/**
	 * Detects whether the current browser is mobile Safari.
	 */
	powerplayer.utils.isIOS = function() {
		return powerplayer.utils.userAgentMatch(/iP(hone|ad|od)/i);
	};
	
	powerplayer.utils.isIPad = function() {
		return powerplayer.utils.userAgentMatch(/iPad/i);
	};

	powerplayer.utils.isIPod = function() {
		return powerplayer.utils.userAgentMatch(/iP(hone|od)/i);
	};
	
	powerplayer.utils.isAndroid = function() {
		return powerplayer.utils.userAgentMatch(/android/i);
	};

	powerplayer.utils.isChrome = function() {
        return powerplayer.utils.userAgentMatch(/Chrome/i);
	}

    powerplayer.utils.isFirefox = function() {
        return powerplayer.utils.userAgentMatch(/Firefox/i);
    }

	/**
	 * Detects whether the current browser is Android 2.0, 2.1 or 2.2 which do
	 * have some support for HTML5
	 */
	powerplayer.utils.isLegacyAndroid = function() {
		return powerplayer.utils.userAgentMatch(/android 2.[012]/i);
	};

	
	powerplayer.utils.isBlackberry = function() {
		return powerplayer.utils.userAgentMatch(/blackberry/i);
	};
	
	/** Matches iOS and Android devices **/	
	powerplayer.utils.isMobile = function() {
		return powerplayer.utils.userAgentMatch(/(iP(hone|ad|od))|android/i);
	}


	powerplayer.utils.getFirstPlaylistItemFromConfig = function(config) {
		var item = {};
		var playlistItem;
		if (config.playlist && config.playlist.length) {
			playlistItem = config.playlist[0];
		} else {
			playlistItem = config;
		}
		item.file = playlistItem.file;
		item.levels = playlistItem.levels;
		item.streamer = playlistItem.streamer;
		item.playlistfile = playlistItem.playlistfile;

		item.provider = playlistItem.provider;
		if (!item.provider) {
			if (item.file
					&& (item.file.toLowerCase().indexOf("youtube.com") > -1 || item.file
							.toLowerCase().indexOf("youtu.be") > -1)) {
				item.provider = "youtube";
			}
			if (item.streamer
					&& item.streamer.toLowerCase().indexOf("rtmp://") == 0) {
				item.provider = "rtmp";
			}
			if (playlistItem.type) {
				item.provider = playlistItem.type.toLowerCase();
			}
		}
		
		if (item.provider == "audio") {
			item.provider = "sound";
		}

		return item;
	}

	/**
	 * Replacement for "outerHTML" getter (not available in FireFox)
	 */
	powerplayer.utils.getOuterHTML = function(element) {
		if (element.outerHTML) {
			return element.outerHTML;
		} else {
			try {
				return new XMLSerializer().serializeToString(element);
			} catch (err) {
				return "";
			}
		}
	};

	/**
	 * Replacement for outerHTML setter (not available in FireFox)
	 */
	powerplayer.utils.setOuterHTML = function(element, html) {
		if (element.outerHTML) {
			element.outerHTML = html;
		} else {
			var el = document.createElement('div');
			el.innerHTML = html;
			var range = document.createRange();
			range.selectNodeContents(el);
			var documentFragment = range.extractContents();
			element.parentNode.insertBefore(documentFragment, element);
			element.parentNode.removeChild(element);
		}
	};

	/**
	 * Detects whether or not the current player has flash capabilities TODO:
	 * Add minimum flash version constraint: 9.0.115
	 */
	powerplayer.utils.hasFlash = function() {
		if (typeof navigator.plugins != "undefined"
			&& typeof navigator.plugins['Shockwave Flash'] != "undefined") {
			return true;
		}
		if (typeof window.ActiveXObject != "undefined") {
			try {
				new ActiveXObject("ShockwaveFlash.ShockwaveFlash");
				return true
			} catch (err) {
			}
		}
		return false;
	};

	/**
	 * Extracts a plugin name from a string
	 */
	powerplayer.utils.getPluginName = function(pluginName) {
		if (pluginName.lastIndexOf("/") >= 0) {
			pluginName = pluginName.substring(pluginName.lastIndexOf("/") + 1,
					pluginName.length);
		}
		if (pluginName.lastIndexOf("-") >= 0) {
			pluginName = pluginName.substring(0, pluginName.lastIndexOf("-"));
		}
		if (pluginName.lastIndexOf(".swf") >= 0) {
			pluginName = pluginName
					.substring(0, pluginName.lastIndexOf(".swf"));
		}
		if (pluginName.lastIndexOf(".js") >= 0) {
			pluginName = pluginName.substring(0, pluginName.lastIndexOf(".js"));
		}
		return pluginName;
	};

	/**
	 * Extracts a plugin version from a string
	 */
	powerplayer.utils.getPluginVersion = function(pluginName) {
		if (pluginName.lastIndexOf("-") >= 0) {
			if (pluginName.lastIndexOf(".js") >= 0) {
				return pluginName.substring(pluginName.lastIndexOf("-") + 1,
						pluginName.lastIndexOf(".js"));
			} else if (pluginName.lastIndexOf(".swf") >= 0) {
				return pluginName.substring(pluginName.lastIndexOf("-") + 1,
						pluginName.lastIndexOf(".swf"));
			} else {
				return pluginName.substring(pluginName.lastIndexOf("-") + 1);
			}
		}
		return "";
	};

	/** Gets an absolute file path based on a relative filepath * */
	powerplayer.utils.getAbsolutePath = function(path, base) {
		if (!powerplayer.utils.exists(base)) {
			base = document.location.href;
		}
		if (!powerplayer.utils.exists(path)) {
			return undefined;
		}
		if (isAbsolutePath(path)) {
			return path;
		}
		var protocol = base.substring(0, base.indexOf("://") + 3);
		var domain = base.substring(protocol.length, base.indexOf('/',
				protocol.length + 1));
		var patharray;
		if (path.indexOf("/") === 0) {
			patharray = path.split("/");
		} else {
			var basepath = base.split("?")[0];
			basepath = basepath.substring(protocol.length + domain.length + 1,
					basepath.lastIndexOf('/'));
			patharray = basepath.split("/").concat(path.split("/"));
		}
		var result = [];
		for ( var i = 0; i < patharray.length; i++) {
			if (!patharray[i] || !powerplayer.utils.exists(patharray[i])
					|| patharray[i] == ".") {
				continue;
			} else if (patharray[i] == "..") {
				result.pop();
			} else {
				result.push(patharray[i]);
			}
		}
		return protocol + domain + "/" + result.join("/");
	};

	function isAbsolutePath(path) {
		if (!powerplayer.utils.exists(path)) {
			return;
		}
		var protocol = path.indexOf("://");
		var queryparams = path.indexOf("?");
		return (protocol > 0 && (queryparams < 0 || (queryparams > protocol)));
	}

	/**
	 * Types of plugin paths
	 */
	powerplayer.utils.pluginPathType = {
		// TODO: enum
		ABSOLUTE : "ABSOLUTE",
		RELATIVE : "RELATIVE",
		CDN : "CDN"
	}

	/*
	 * Test cases getPathType('hd') getPathType('hd-1') getPathType('hd-1.4')
	 * 
	 * getPathType('http://plugins.longtailvideo.com/5/hd/hd.swf')
	 * getPathType('http://plugins.longtailvideo.com/5/hd/hd-1.swf')
	 * getPathType('http://plugins.longtailvideo.com/5/hd/hd-1.4.swf')
	 * 
	 * getPathType('./hd.swf') getPathType('./hd-1.swf')
	 * getPathType('./hd-1.4.swf')
	 */
	powerplayer.utils.getPluginPathType = function(path) {
		if (typeof path != "string") {
			return;
		}
		path = path.split("?")[0];
		var protocol = path.indexOf("://");
		if (protocol > 0) {
			return powerplayer.utils.pluginPathType.ABSOLUTE;
		}
		var folder = path.indexOf("/");
		var extension = powerplayer.utils.extension(path);
		if (protocol < 0 && folder < 0 && (!extension || !isNaN(extension))) {
			return powerplayer.utils.pluginPathType.CDN;
		}
		return powerplayer.utils.pluginPathType.RELATIVE;
	};

	powerplayer.utils.mapEmpty = function(map) {
		for ( var val in map) {
			return false;
		}
		return true;
	};

	powerplayer.utils.mapLength = function(map) {
		var result = 0;
		for ( var val in map) {
			result++;
		}
		return result;
	};

	/** Logger * */
	powerplayer.utils.log = function(msg, obj) {
		if (typeof console != "undefined" && typeof console.log != "undefined") {
			if (obj) {
				console.log(msg, obj);
			} else {
				console.log(msg);
			}
		}
	};

	/**
	 * 
	 * @param {Object}
	 *            domelement
	 * @param {Object}
	 *            styles
	 * @param {Object}
	 *            debug
	 */
	powerplayer.utils.css = function(domelement, styles, debug) {
		if (powerplayer.utils.exists(domelement)) {
			for ( var style in styles) {
				try {
					if (typeof styles[style] === "undefined") {
						continue;
					} else if (typeof styles[style] == "number"
							&& !(style == "zIndex" || style == "opacity")) {
						if (isNaN(styles[style])) {
							continue;
						}
						if (style.match(/color/i)) {
							styles[style] = "#"
									+ powerplayer.utils.strings.pad(styles[style]
											.toString(16), 6);
						} else {
							styles[style] = Math.ceil(styles[style]) + "px";
						}
					}
					domelement.style[style] = styles[style];
				} catch (err) {
				}
			}
		}
	};

	powerplayer.utils.isYouTube = function(path) {
		return (path.indexOf("youtube.com") > -1 || path.indexOf("youtu.be") > -1);
	};

	/**
	 * 
	 * @param {Object}
	 *            domelement
	 * @param {Object}
	 *            xscale
	 * @param {Object}
	 *            yscale
	 * @param {Object}
	 *            xoffset
	 * @param {Object}
	 *            yoffset
	 */
	powerplayer.utils.transform = function(domelement, xscale, yscale, xoffset, yoffset) {
		// Set defaults
		if (!powerplayer.utils.exists(xscale)) xscale = 1;
		if (!powerplayer.utils.exists(yscale)) yscale = 1;
		if (!powerplayer.utils.exists(xoffset)) xoffset = 0;
		if (!powerplayer.utils.exists(yoffset)) yoffset = 0;
		
		if (xscale == 1 && yscale == 1 && xoffset == 0 && yoffset == 0) {
			domelement.style.webkitTransform = "";
			domelement.style.MozTransform = "";
			domelement.style.OTransform = "";
		} else {
			var value = "scale("+xscale+","+yscale+") translate("+xoffset+"px,"+yoffset+"px)";
			
			domelement.style.webkitTransform = value;
			domelement.style.MozTransform = value;
			domelement.style.OTransform = value;
		}
	};

	/**
	 * Stretches domelement based on stretching. parentWidth, parentHeight,
	 * elementWidth, and elementHeight are required as the elements dimensions
	 * change as a result of the stretching. Hence, the original dimensions must
	 * always be supplied.
	 * 
	 * @param {String}
	 *            stretching
	 * @param {DOMElement}
	 *            domelement
	 * @param {Number}
	 *            parentWidth
	 * @param {Number}
	 *            parentHeight
	 * @param {Number}
	 *            elementWidth
	 * @param {Number}
	 *            elementHeight
	 */
	powerplayer.utils.stretch = function(stretching, domelement, parentWidth,
			parentHeight, elementWidth, elementHeight) {
		if (typeof parentWidth == "undefined"
				|| typeof parentHeight == "undefined"
				|| typeof elementWidth == "undefined"
				|| typeof elementHeight == "undefined") {
			return;
		}
		var xscale = parentWidth / elementWidth;
		var yscale = parentHeight / elementHeight;
		var x = 0;
		var y = 0;
		var transform = false;
		var style = {};
		
		if (domelement.parentElement) {
			domelement.parentElement.style.overflow = "hidden";
		}
		
		powerplayer.utils.transform(domelement);

		switch (stretching.toUpperCase()) {
		case powerplayer.utils.stretching.NONE:
			// Maintain original dimensions
			style.width = elementWidth;
			style.height = elementHeight;
			style.top = (parentHeight - style.height) / 2;
			style.left = (parentWidth - style.width) / 2;
			break;
		case powerplayer.utils.stretching.UNIFORM:
			// Scale on the dimension that would overflow most
			if (xscale > yscale) {
				// Taller than wide
				style.width = elementWidth * yscale;
				style.height = elementHeight * yscale;
				if (style.width/parentWidth > 0.95) {
					transform = true;
					xscale = Math.ceil(100 * parentWidth / style.width) / 100;
					yscale = 1;
					style.width = parentWidth;
				}
			} else {
				// Wider than tall
				style.width = elementWidth * xscale;
				style.height = elementHeight * xscale;
				if (style.height/parentHeight > 0.95) {
					transform = true;
					xscale = 1;
					yscale = Math.ceil(100 * parentHeight / style.height) / 100;
					style.height = parentHeight;
				}
			}
			style.top = (parentHeight - style.height) / 2;
			style.left = (parentWidth - style.width) / 2;
			break;
		case powerplayer.utils.stretching.FILL:
			// Scale on the smaller dimension and crop
			if (xscale > yscale) {
				style.width = elementWidth * xscale;
				style.height = elementHeight * xscale;
			} else {
				style.width = elementWidth * yscale;
				style.height = elementHeight * yscale;
			}
			style.top = (parentHeight - style.height) / 2;
			style.left = (parentWidth - style.width) / 2;
			break;
		case powerplayer.utils.stretching.EXACTFIT:
			// Distort to fit
//			powerplayer.utils.transform(domelement, [ "scale(", xscale, ",",
//					yscale, ")", " translate(0px,0px)" ].join(""));
			style.width = elementWidth;
			style.height = elementHeight;
			
		    var xoff = Math.round((elementWidth / 2) * (1-1/xscale));
	        var yoff = Math.round((elementHeight / 2) * (1-1/yscale));
			
	        transform = true;
			//style.width = style.height = "100%";
			style.top = style.left = 0;

			break;
		default:
			break;
		}

		if (transform) {
			powerplayer.utils.transform(domelement, xscale, yscale, xoff, yoff);
		}

		powerplayer.utils.css(domelement, style);
	};

	// TODO: enum
	powerplayer.utils.stretching = {
		NONE : "NONE",
		FILL : "FILL",
		UNIFORM : "UNIFORM",
		EXACTFIT : "EXACTFIT"
	};

	/**
	 * Recursively traverses nested object, replacing key names containing a
	 * search string with a replacement string.
	 * 
	 * @param searchString
	 *            The string to search for in the object's key names
	 * @param replaceString
	 *            The string to replace in the object's key names
	 * @returns The modified object.
	 */
	powerplayer.utils.deepReplaceKeyName = function(obj, searchString, replaceString) {
		switch (powerplayer.utils.typeOf(obj)) {
		case "array":
			for ( var i = 0; i < obj.length; i++) {
				obj[i] = powerplayer.utils.deepReplaceKeyName(obj[i],
						searchString, replaceString);
			}
			break;
		case "object":
			for ( var key in obj) {
				var searches, replacements;
				if (searchString instanceof Array && replaceString instanceof Array) {
					if (searchString.length != replaceString.length)
						continue;
					else {
						searches = searchString;
						replacements = replaceString;
					}
				} else {
					searches = [searchString];
					replacements = [replaceString];
				}
				var newkey = key;
				for (var i=0; i < searches.length; i++) {
					newkey = newkey.replace(new RegExp(searchString[i], "g"), replaceString[i]);
				}
				obj[newkey] = powerplayer.utils.deepReplaceKeyName(obj[key], searchString, replaceString);
				if (key != newkey) {
					delete obj[key];
				}
			}
			break;
		}
		return obj;
	}

	/**
	 * Returns true if an element is found in a given array
	 * 
	 * @param array
	 *            The array to search
	 * @param search
	 *            The element to search
	 */
	powerplayer.utils.isInArray = function(array, search) {
		if (!(array) || !(array instanceof Array)) {
			return false;
		}

		for ( var i = 0; i < array.length; i++) {
			if (search === array[i]) {
				return true;
			}
		}

		return false;
	}

	/**
	 * Returns true if the value of the object is null, undefined or the empty
	 * string
	 * 
	 * @param a
	 *            The variable to inspect
	 */
	powerplayer.utils.exists = function(a) {
		switch (typeof (a)) {
		case "string":
			return (a.length > 0);
			break;
		case "object":
			return (a !== null);
		case "undefined":
			return false;
		}
		return true;
	}
	
	/**
	 * Removes all of an HTML container's child nodes
	 **/
	powerplayer.utils.empty = function(elem) {
		if (typeof elem.hasChildNodes == "function") {
			while(elem.hasChildNodes()) {
				elem.removeChild(elem.firstChild);
			}
		}
	}
	
	/**
	 * Cleans up a css dimension (e.g. '420px') and returns an integer.
	 */
	powerplayer.utils.parseDimension = function(dimension) {
		if (typeof dimension == "string") {
			if (dimension === "") {
				return 0;
			} else if (dimension.lastIndexOf("%") > -1) {
				return dimension;
			} else {
				return parseInt(dimension.replace("px", ""), 10);
			}
		}
		return dimension;
	}
	
	/**
	 * Returns dimensions (x,y,width,height) of a display object
	 */
	powerplayer.utils.getDimensions = function(obj) {
		if (obj && obj.style) {
			return {
				x: powerplayer.utils.parseDimension(obj.style.left),
				y: powerplayer.utils.parseDimension(obj.style.top),
				width: powerplayer.utils.parseDimension(obj.style.width),
				height: powerplayer.utils.parseDimension(obj.style.height)
			};
		} else {
			return {};
		}
	}

	/**
	 * Gets the clientWidth of an element, or returns its style.width
	 */
	powerplayer.utils.getElementWidth = function(obj) {
		if (!obj) {
			return null;
		} else if (obj == document.body) {
			return powerplayer.utils.parentNode(obj).clientWidth;
		} else if (obj.clientWidth > 0) {
			return obj.clientWidth;
		} else if (obj.style) {
			return powerplayer.utils.parseDimension(obj.style.width);
		} else {
			return null;
		}
	}

	/**
	 * Gets the clientHeight of an element, or returns its style.height
	 */
	powerplayer.utils.getElementHeight = function(obj) {
		if (!obj) {
			return null;
		} else if (obj == document.body) {
			return powerplayer.utils.parentNode(obj).clientHeight;
		} else if (obj.clientHeight > 0) {
			return obj.clientHeight;
		} else if (obj.style) {
			return powerplayer.utils.parseDimension(obj.style.height);
		} else {
			return null;
		}
	}

	
	
	/** Format the elapsed / remaining text. **/
	powerplayer.utils.timeFormat = function(sec) {
		str = "00:00";
		if (sec > 0) {
			str = Math.floor(sec / 60) < 10 ? "0" + Math.floor(sec / 60) + ":" : Math.floor(sec / 60) + ":";
			str += Math.floor(sec % 60) < 10 ? "0" + Math.floor(sec % 60) : Math.floor(sec % 60);
		}
		return str;
	}
	

	/** Returns true if the player should use the browser's native fullscreen mode **/
	powerplayer.utils.useNativeFullscreen = function() {
		//return powerplayer.utils.isIOS(); // Only use iOS for now -- Safari's video.webkitRequestFullScreen is buggy
		return (navigator && navigator.vendor && navigator.vendor.indexOf("Apple") == 0);
	}

	/** Returns an element's parent element.  If no parent is available, return the element itself **/
	powerplayer.utils.parentNode = function(element) {
		if (!element) {
			return document.body;
		} else if (element.parentNode) {
			return element.parentNode;
		} else if (element.parentElement) {
			return element.parentElement;
		} else {
			return element;
		}
	}
	
	/** Replacement for getBoundingClientRect, which isn't supported in iOS 3.1.2 **/
	powerplayer.utils.getBoundingClientRect = function(element) {
		if (typeof element.getBoundingClientRect == "function") {
			return element.getBoundingClientRect();
		} else {
			return { 
				left: element.offsetLeft + document.body.scrollLeft, 
				top: element.offsetTop + document.body.scrollTop, 
				width: element.offsetWidth, 
				height: element.offsetHeight
			};
		}
	}
	
	/* Normalizes differences between Flash and HTML5 internal players' event responses. */
	powerplayer.utils.translateEventResponse = function(type, eventProperties) {
		var translated = powerplayer.utils.extend({}, eventProperties);
		if (type == powerplayer.api.events.POWERPLAYER_FULLSCREEN && !translated.fullscreen) {
			translated.fullscreen = translated.message == "true" ? true : false;
			delete translated.message;
		} else if (typeof translated.data == "object") {
			// Takes ViewEvent "data" block and moves it up a level
			translated = powerplayer.utils.extend(translated, translated.data);
			delete translated.data;
		} else if (typeof translated.metadata == "object") {
			powerplayer.utils.deepReplaceKeyName(translated.metadata, ["__dot__","__spc__","__dsh__"], ["."," ","-"]);
		}
		
		var rounders = ["position", "duration", "offset"];
		for (var rounder in rounders) {
			if (translated[rounders[rounder]]) {
				translated[rounders[rounder]] = Math.round(translated[rounders[rounder]] * 1000) / 1000;
			}
		}
		
		return translated;
	}
	
	powerplayer.utils.saveCookie = function(name, value) {
		document.cookie = "powerplayer." + name + "=" + value + "; path=/";
	}

	powerplayer.utils.getCookies = function() {
		var pwCookies = {};
		var cookies = document.cookie.split('; ');
		for (var i=0; i<cookies.length; i++) {
			var split = cookies[i].split('=');
			if (split[0].indexOf("powerplayer.") == 0) {
				pwCookies[split[0].substring(9, split[0].length)] = split[1];
			}
		}
		return pwCookies;
	}
	
	powerplayer.utils.readCookie = function(name) {
		return powerplayer.utils.getCookies()[name];
	}

})(powerplayer);
/**
 * Utility methods for the PW Player.
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {
	var _animations = {};
	
	powerplayer.utils.animations = function() {
	};
	
	powerplayer.utils.animations.transform = function(domelement, value) {
		domelement.style.webkitTransform = value;
		domelement.style.MozTransform = value;
		domelement.style.OTransform = value;
		domelement.style.msTransform = value;
	};
	
	powerplayer.utils.animations.transformOrigin = function(domelement, value) {
		domelement.style.webkitTransformOrigin = value;
		domelement.style.MozTransformOrigin = value;
		domelement.style.OTransformOrigin = value;
		domelement.style.msTransformOrigin = value;
	};
	
	powerplayer.utils.animations.rotate = function(domelement, deg) {
		powerplayer.utils.animations.transform(domelement, ["rotate(", deg, "deg)"].join(""));
	};
	
	powerplayer.utils.cancelAnimation = function(domelement) {
		delete _animations[domelement.id];
	};
	
	powerplayer.utils.fadeTo = function(domelement, endAlpha, time, startAlpha, delay, startTime) {
		// Interrupting
		if (_animations[domelement.id] != startTime && powerplayer.utils.exists(startTime)) {
			return;
		}
		// No need to fade if the opacity is already where we're going
		if (domelement.style.opacity == endAlpha) {
			return;
		}
		
		var currentTime = new Date().getTime();
		if (startTime > currentTime) {
			setTimeout(function() {
				powerplayer.utils.fadeTo(domelement, endAlpha, time, startAlpha, 0, startTime);
			}, startTime - currentTime);
		}
		if (domelement.style.display == "none") {
			domelement.style.display = "block";
		}
		if (!powerplayer.utils.exists(startAlpha)) {
			startAlpha = domelement.style.opacity === "" ? 1 : domelement.style.opacity;
		}
		if (domelement.style.opacity == endAlpha && domelement.style.opacity !== "" && powerplayer.utils.exists(startTime)) {
			if (endAlpha === 0) {
				domelement.style.display = "none";
			}
			return;
		}
		if (!powerplayer.utils.exists(startTime)) {
			startTime = currentTime;
			_animations[domelement.id] = startTime;
		}
		if (!powerplayer.utils.exists(delay)) {
			delay = 0;
		}
		var percentTime = (time > 0) ? ((currentTime - startTime) / (time * 1000)) : 0;
		percentTime = percentTime > 1 ? 1 : percentTime;
		var delta = endAlpha - startAlpha;
		var alpha = startAlpha + (percentTime * delta);
		if (alpha > 1) {
			alpha = 1;
		} else if (alpha < 0) {
			alpha = 0;
		}
		domelement.style.opacity = alpha;
		if (delay > 0) {
			_animations[domelement.id] = startTime + delay * 1000;
			powerplayer.utils.fadeTo(domelement, endAlpha, time, startAlpha, 0, _animations[domelement.id]);
			return;
		}
		setTimeout(function() {
			powerplayer.utils.fadeTo(domelement, endAlpha, time, startAlpha, 0, startTime);
		}, 10);
	};
})(powerplayer);
/**
 * Arrays utility class
 * 
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	powerplayer.utils.arrays = function(){};
	
	/**
	 * Finds an element in an Array
	 * 
	 * @param {Object} haystack
	 * @param {Object} needle
	 * @return {Number} int
	 */
	powerplayer.utils.arrays.indexOf = function(haystack, needle){
		for (var i = 0; i < haystack.length; i++){
			if (haystack[i] == needle){
				return i;
			}
		}
		return -1;
	};
	
	/**
	 * Removes and element from an array
	 * 
	 * @param {Object} haystack
	 * @param {Object} needle
	 */
	powerplayer.utils.arrays.remove = function(haystack, needle){
		var neeedleIndex = powerplayer.utils.arrays.indexOf(haystack, needle);
		if (neeedleIndex > -1){
			haystack.splice(neeedleIndex, 1);
		}
	};
})(powerplayer);
/**
 * pw Player Media Extension to Mime Type mapping
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {
	powerplayer.utils.extensionmap = {
		"3gp": {
			"html5": "video/3gpp",
			"flash": "video"
		},
		"3gpp": {
			"html5": "video/3gpp"
		},
		"3g2": {
			"html5": "video/3gpp2",
			"flash": "video"
		},
		"3gpp2": {
			"html5": "video/3gpp2"
		},
		"flv": {
            "html5": "video/flv",
			"flash": "video"
		},
		"f4a": {
			"html5": "audio/mp4"
		},
		"f4b": {
			"html5": "audio/mp4",
			"flash": "video"
		},
		"f4v": {
			"html5": "video/mp4",
			"flash": "video"
		},
		"mov": {
			"html5": "video/quicktime",
			"flash": "video"
		},
		"m4a": {
			"html5": "audio/mp4",
			"flash": "video"
		},
		"m4b": {
			"html5": "audio/mp4"
		},
		"m4p": {
			"html5": "audio/mp4"
		},
		"m4v": {
			"html5": "video/mp4",
			"flash": "video"
		},
		"mp4": {
			"html5": "video/mp4",
			"flash": "video"
		},
		"rbs":{
			"flash": "sound"
		},
		"aac": {
			"html5": "audio/aac",
			"flash": "video"
		},
		"mp3": {
			"html5": "audio/mp3",
			"flash": "sound"
		},
		"ogg": {
			"html5": "audio/ogg"
		},
		"oga": {
			"html5": "audio/ogg"
		},
		"ogv": {
			"html5": "video/ogg"
		},
		"webm": {
			"html5": "video/webm"
		},
		"m3u8": {
			"html5": "audio/x-mpegurl",
            "flash": "video"
		},
		"gif": {
			"flash": "image"
		},
		"jpeg": {
			"flash": "image"
		},
		"jpg": {
			"flash": "image"
		},
		"swf":{
			"flash": "image"
		},
		"png":{
			"flash": "image"
		},
		"wav":{
			"html5": "audio/x-wav"
		}
	};
})(powerplayer);
/**
 * Created by qinws on 2019/1/4.
 */
(function(powerplayer) {
    //TODO: Enum
    powerplayer.utils.loaderstatus = {
        NEW: "NEW",
        LOADING: "LOADING",
        ERROR: "ERROR",
        COMPLETE: "COMPLETE"
    };

    powerplayer.utils.linkloader = function(url) {
        var _status = powerplayer.utils.loaderstatus.NEW;
        var _eventDispatcher = new powerplayer.events.eventdispatcher();
        powerplayer.utils.extend(this, _eventDispatcher);

        this.load = function() {
            if (_status == powerplayer.utils.loaderstatus.NEW) {
                _status = powerplayer.utils.loaderstatus.LOADING;
                var linkTag = document.createElement("link");
                linkTag.rel = 'stylesheet';
                linkTag.type = 'text/css';
                // Most browsers
                linkTag.onload = function(evt) {
                    _status = powerplayer.utils.loaderstatus.COMPLETE;
                    _eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
                }
                linkTag.onerror = function(evt) {
                    _status = powerplayer.utils.loaderstatus.ERROR;
                    _eventDispatcher.sendEvent(powerplayer.events.ERROR);
                }
                // IE
                linkTag.onreadystatechange = function() {
                    if (linkTag.readyState == 'loaded' || linkTag.readyState == 'complete') {
                        _status = powerplayer.utils.loaderstatus.COMPLETE;
                        _eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
                    }
                    // Error?
                }
                document.getElementsByTagName("head")[0].appendChild(linkTag);
                linkTag.href = url;
            }

        };

        this.getStatus = function() {
            return _status;
        }
    }
})(powerplayer);
/**
 * Parser for the PW Player.
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {

    powerplayer.utils.mediaparser = function() {};

	var elementAttributes = {
		element: {
			width: 'width',
			height: 'height',
			id: 'id',
			'class': 'className',
			name: 'name'
		},
		media: {
			src: 'file',
			preload: 'preload',
			autoplay: 'autostart',
			loop: 'repeat',
			controls: 'controls'
		},
		source: {
			src: 'file',
			type: 'type',
			media: 'media',
			'data-pw-width': 'width',
			'data-pw-bitrate': 'bitrate'
				
		},
		video: {
			poster: 'image'
		}
	};
	
	var parsers = {};
	
	powerplayer.utils.mediaparser.parseMedia = function(element) {
		return parseElement(element);
	};
	
	function getAttributeList(elementType, attributes) {
		if (!powerplayer.utils.exists(attributes)) {
			attributes = elementAttributes[elementType];
		} else {
			powerplayer.utils.extend(attributes, elementAttributes[elementType]);
		}
		return attributes;
	}
	
	function parseElement(domElement, attributes) {
		if (parsers[domElement.tagName.toLowerCase()] && !powerplayer.utils.exists(attributes)) {
			return parsers[domElement.tagName.toLowerCase()](domElement);
		} else {
			attributes = getAttributeList('element', attributes);
			var configuration = {};
			for (var attribute in attributes) {
				if (attribute != "length") {
					var value = domElement.getAttribute(attribute);
					if (powerplayer.utils.exists(value)) {
						configuration[attributes[attribute]] = value;
					}
				}
			}
			var bgColor = domElement.style['#background-color'];
			if (bgColor && !(bgColor == "transparent" || bgColor == "rgba(0, 0, 0, 0)")) {
				configuration.screencolor = bgColor;
			}
			return configuration;
		}
	}
	
	function parseMediaElement(domElement, attributes) {
		attributes = getAttributeList('media', attributes);
		var sources = [];
		var sourceTags = powerplayer.utils.selectors("source", domElement);
		for (var i in sourceTags) {
			if (!isNaN(i)){
				sources.push(parseSourceElement(sourceTags[i]));					
			}
		}
		var configuration = parseElement(domElement, attributes);
		if (powerplayer.utils.exists(configuration.file)) {
			sources[0] = {
				'file': configuration.file
			};
		}
		configuration.levels = sources;
		return configuration;
	}
	
	function parseSourceElement(domElement, attributes) {
		attributes = getAttributeList('source', attributes);
		var result = parseElement(domElement, attributes);
		result.width = result.width ? result.width : 0;
		result.bitrate = result.bitrate ? result.bitrate : 0;
		return result;
	}
	
	function parseVideoElement(domElement, attributes) {
		attributes = getAttributeList('video', attributes);
		var result = parseMediaElement(domElement, attributes);
		return result;
	}
	
	parsers.media = parseMediaElement;
	parsers.audio = parseMediaElement;
	parsers.source = parseSourceElement;
	parsers.video = parseVideoElement;
	
	
})(powerplayer);
/**
 * Loads a <script> tag
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	//TODO: Enum
	powerplayer.utils.loaderstatus = {
		NEW: "NEW",
		LOADING: "LOADING",
		ERROR: "ERROR",
		COMPLETE: "COMPLETE"
	};
	
	powerplayer.utils.scriptloader = function(url) {
		var _status = powerplayer.utils.loaderstatus.NEW;
		var _eventDispatcher = new powerplayer.events.eventdispatcher();
		powerplayer.utils.extend(this, _eventDispatcher);
		
		this.load = function() {
			if (_status == powerplayer.utils.loaderstatus.NEW) {
				_status = powerplayer.utils.loaderstatus.LOADING;
				var scriptTag = document.createElement("script");
				// Most browsers
				scriptTag.onload = function(evt) {
					_status = powerplayer.utils.loaderstatus.COMPLETE;
					_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
				}
				scriptTag.onerror = function(evt) {
					_status = powerplayer.utils.loaderstatus.ERROR;
					_eventDispatcher.sendEvent(powerplayer.events.ERROR);
				}
				// IE
				scriptTag.onreadystatechange = function() {
					if (scriptTag.readyState == 'loaded' || scriptTag.readyState == 'complete') {
						_status = powerplayer.utils.loaderstatus.COMPLETE;
						_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
					}
					// Error?
				}
				document.getElementsByTagName("head")[0].appendChild(scriptTag);
				scriptTag.src = url;
			}
			
		};
		
		this.getStatus = function() {
			return _status;
		}
	}
})(powerplayer);
/**
 * Selectors for the PW Player.
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {
	powerplayer.utils.selectors = function(selector, parent) {
		if (!powerplayer.utils.exists(parent)) {
			parent = document;
		}
		selector = powerplayer.utils.strings.trim(selector);
		var selectType = selector.charAt(0);
		if (selectType == "#") {
			return parent.getElementById(selector.substr(1));
		} else if (selectType == ".") {
			if (parent.getElementsByClassName) {
				return parent.getElementsByClassName(selector.substr(1));
			} else {
				return powerplayer.utils.selectors.getElementsByTagAndClass("*", selector.substr(1));
			}
		} else {
			if (selector.indexOf(".") > 0) {
				var selectors = selector.split(".");
				return powerplayer.utils.selectors.getElementsByTagAndClass(selectors[0], selectors[1]);
			} else {
				return parent.getElementsByTagName(selector);
			}
		}
		return null;
	};
	
	powerplayer.utils.selectors.getElementsByTagAndClass = function(tagName, className, parent) {
		var elements = [];
		if (!powerplayer.utils.exists(parent)) {
			parent = document;
		}
		var selected = parent.getElementsByTagName(tagName);
		for (var i = 0; i < selected.length; i++) {
			if (powerplayer.utils.exists(selected[i].className)) {
				var classes = selected[i].className.split(" ");
				for (var classIndex = 0; classIndex < classes.length; classIndex++) {
					if (classes[classIndex] == className) {
						elements.push(selected[i]);
					}
				}
			}
		}
		return elements;
	};
})(powerplayer);
/**
 * String utilities for the PW Player.
 *
 * @author zach
 * @version 5.8
 */
(function(powerplayer) {

	powerplayer.utils.strings = function() {
	};
	
	/** Removes whitespace from the beginning and end of a string **/
	powerplayer.utils.strings.trim = function(inputString) {
		return inputString.replace(/^\s*/, "").replace(/\s*$/, "");
	};
	
	/**
	 * Pads a string
	 * @param {String} string
	 * @param {Number} length
	 * @param {String} padder
	 */
	powerplayer.utils.strings.pad = function (string, length, padder) {
		if (!padder){
			padder = "0";
		}
		while (string.length < length) {
			string = padder + string;
		}
		return string;
	}
	
		/**
	 * Basic serialization: string representations of booleans and numbers are returned typed;
	 * strings are returned urldecoded.
	 *
	 * @param {String} val	String value to serialize.
	 * @return {Object}		The original value in the correct primitive type.
	 */
	powerplayer.utils.strings.serialize = function(val) {
		if (val == null) {
			return null;
		} else if (val == 'true') {
			return true;
		} else if (val == 'false') {
			return false;
		} else if (isNaN(Number(val)) || val.length > 5 || val.length == 0) {
			return val;
		} else {
			return Number(val);
		}
	}
	
	
	/**
	 * Convert a time-representing string to a number.
	 *
	 * @param {String}	The input string. Supported are 00:03:00.1 / 03:00.1 / 180.1s / 3.2m / 3.2h
	 * @return {Number}	The number of seconds.
	 */
	powerplayer.utils.strings.seconds = function(str) {
		str = str.replace(',', '.');
		var arr = str.split(':');
		var sec = 0;
		if (str.substr(-1) == 's') {
			sec = Number(str.substr(0, str.length - 1));
		} else if (str.substr(-1) == 'm') {
			sec = Number(str.substr(0, str.length - 1)) * 60;
		} else if (str.substr(-1) == 'h') {
			sec = Number(str.substr(0, str.length - 1)) * 3600;
		} else if (arr.length > 1) {
			sec = Number(arr[arr.length - 1]);
			sec += Number(arr[arr.length - 2]) * 60;
			if (arr.length == 3) {
				sec += Number(arr[arr.length - 3]) * 3600;
			}
		} else {
			sec = Number(str);
		}
		return sec;
	}
	
	
	/**
	 * Get the value of a case-insensitive attribute in an XML node
	 * @param {XML} xml
	 * @param {String} attribute
	 * @return {String} Value
	 */
	powerplayer.utils.strings.xmlAttribute = function(xml, attribute) {
		for (var attrib = 0; attrib < xml.attributes.length; attrib++) {
			if (xml.attributes[attrib].name && xml.attributes[attrib].name.toLowerCase() == attribute.toLowerCase()) 
				return xml.attributes[attrib].value.toString();
		}
		return "";
	}
	
	/**
	 * Converts a JSON object into its string representation.
	 * @param obj {Object} String, Number, Array or nested Object to serialize
	 * Serialization code borrowed from 
	 */
	powerplayer.utils.strings.jsonToString = function(obj) {
		// Use browser's native JSON implementation if it exists.
		var JSON = JSON || {}
		if (JSON && JSON.stringify) {
				return JSON.stringify(obj);
		}

		var type = typeof (obj);
		if (type != "object" || obj === null) {
			// Object is string or number
			if (type == "string") {
				obj = '"'+obj.replace(/"/g, '\\"')+'"';
			} else {
				return String(obj);
			}
		}
		else {
			// Object is an array or object
			var toReturn = [],
				isArray = (obj && obj.constructor == Array);
				
			for (var item in obj) {
				var value = obj[item];
				
				switch (typeof(value)) {
					case "string":
						value = '"' + value.replace(/"/g, '\\"') + '"';
						break;
					case "object":
						if (powerplayer.utils.exists(value)) {
							value = powerplayer.utils.strings.jsonToString(value);
						}
						break;
				}
				if (isArray) {
					// Array
					if (typeof(value) != "function") {
						toReturn.push(String(value));
					}
				} else {
					// Object
					if (typeof(value) != "function") {
						toReturn.push('"' + item + '":' + String(value));
					}
				}
			}
			
			if (isArray) {
				return "[" + String(toReturn) + "]";
			} else {
				return "{" + String(toReturn) + "}";
			}
		}
	}
	
})(powerplayer);
/**
 * Utility methods for the PW Player.
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {
	var _colorPattern = new RegExp(/^(#|0x)[0-9a-fA-F]{3,6}/);
	
	powerplayer.utils.typechecker = function(value, type) {
		type = !powerplayer.utils.exists(type) ? _guessType(value) : type;
		return _typeData(value, type);
	};
	
	function _guessType(value) {
		var bools = ["true", "false", "t", "f"];
		if (bools.toString().indexOf(value.toLowerCase().replace(" ", "")) >= 0) {
			return "boolean";
		} else if (_colorPattern.test(value)) {
			return "color";
		} else if (!isNaN(parseInt(value, 10)) && parseInt(value, 10).toString().length == value.length) {
			return "integer";
		} else if (!isNaN(parseFloat(value)) && parseFloat(value).toString().length == value.length) {
			return "float";
		}
		return "string";
	}
	
	function _typeData(value, type) {
		if (!powerplayer.utils.exists(type)) {
			return value;
		}
		
		switch (type) {
			case "color":
				if (value.length > 0) {
					return _stringToColor(value);
				}
				return null;
			case "integer":
				return parseInt(value, 10);
			case "float":
				return parseFloat(value);
			case "boolean":
				if (value.toLowerCase() == "true") {
					return true;
				} else if (value == "1") {
					return true;
				}
				return false;
		}
		return value;
	}
	
	function _stringToColor(value) {
		switch (value.toLowerCase()) {
			case "blue":
				return parseInt("0000FF", 16);
			case "green":
				return parseInt("00FF00", 16);
			case "red":
				return parseInt("FF0000", 16);
			case "cyan":
				return parseInt("00FFFF", 16);
			case "magenta":
				return parseInt("FF00FF", 16);
			case "yellow":
				return parseInt("FFFF00", 16);
			case "black":
				return parseInt("000000", 16);
			case "white":
				return parseInt("FFFFFF", 16);
			default:
				value = value.replace(/(#|0x)?([0-9A-F]{3,6})$/gi, "$2");
				if (value.length == 3) {
					value = value.charAt(0) + value.charAt(0) + value.charAt(1) + value.charAt(1) + value.charAt(2) + value.charAt(2);
				}
				return parseInt(value, 16);
		}
		
		return parseInt("000000", 16);
	}
	
})(powerplayer);
/**
 * Event namespace defintion for the PW Player
 *
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	powerplayer.events = function() {
	};
	
	powerplayer.events.COMPLETE = "COMPLETE";
	powerplayer.events.ERROR = "ERROR";
})(powerplayer);
/**
 * Event dispatcher for the PW Player
 *
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	powerplayer.events.eventdispatcher = function(debug) {
		var _debug = debug;
		var _listeners;
		var _globallisteners;
		
		/** Clears all event listeners **/
		this.resetEventListeners = function() {
			_listeners = {};
			_globallisteners = [];
		};
		
		this.resetEventListeners();
		
		/** Add an event listener for a specific type of event. **/
		this.addEventListener = function(type, listener, count) {
			try {
				if (!powerplayer.utils.exists(_listeners[type])) {
					_listeners[type] = [];
				}
				
				if (typeof(listener) == "string") {
					eval("listener = " + listener);
				}
				_listeners[type].push({
					listener: listener,
					count: count
				});
			} catch (err) {
				powerplayer.utils.log("error", err);
			}
			return false;
		};
		
		
		/** Remove an event listener for a specific type of event. **/
		this.removeEventListener = function(type, listener) {
			if (!_listeners[type]) {
				return;
			}
			try {
				for (var listenerIndex = 0; listenerIndex < _listeners[type].length; listenerIndex++) {
					if (_listeners[type][listenerIndex].listener.toString() == listener.toString()) {
						_listeners[type].splice(listenerIndex, 1);
						break;
					}
				}
			} catch (err) {
				powerplayer.utils.log("error", err);
			}
			return false;
		};
		
		/** Add an event listener for all events. **/
		this.addGlobalListener = function(listener, count) {
			try {
				if (typeof(listener) == "string") {
					eval("listener = " + listener);
				}
				_globallisteners.push({
					listener: listener,
					count: count
				});
			} catch (err) {
				powerplayer.utils.log("error", err);
			}
			return false;
		};
		
		/** Add an event listener for all events. **/
		this.removeGlobalListener = function(listener) {
			if (!listener) {
				return;
			}
			try {
				for (var globalListenerIndex = 0; globalListenerIndex < _globallisteners.length; globalListenerIndex++) {
					if (_globallisteners[globalListenerIndex].listener.toString() == listener.toString()) {
						_globallisteners.splice(globalListenerIndex, 1);
						break;
					}
				}
			} catch (err) {
				powerplayer.utils.log("error", err);
			}
			return false;
		};
		
		
		/** Send an event **/
		this.sendEvent = function(type, data) {
			if (!powerplayer.utils.exists(data)) {
				data = {};
			}
			if (_debug) {
				powerplayer.utils.log(type, data);
			}
			if (typeof _listeners[type] != "undefined") {
				for (var listenerIndex = 0; listenerIndex < _listeners[type].length; listenerIndex++) {
					try {
						_listeners[type][listenerIndex].listener(data);
					} catch (err) {
						powerplayer.utils.log("There was an error while handling a listener: " + err.toString(), _listeners[type][listenerIndex].listener);
					}
					if (_listeners[type][listenerIndex]) {
						if (_listeners[type][listenerIndex].count === 1) {
							delete _listeners[type][listenerIndex];
						} else if (_listeners[type][listenerIndex].count > 0) {
							_listeners[type][listenerIndex].count = _listeners[type][listenerIndex].count - 1;
						}
					}
				}
			}
			for (var globalListenerIndex = 0; globalListenerIndex < _globallisteners.length; globalListenerIndex++) {
				try {
					_globallisteners[globalListenerIndex].listener(data);
				} catch (err) {
					powerplayer.utils.log("There was an error while handling a listener: " + err.toString(), _globallisteners[globalListenerIndex].listener);
				}
				if (_globallisteners[globalListenerIndex]) {
					if (_globallisteners[globalListenerIndex].count === 1) {
						delete _globallisteners[globalListenerIndex];
					} else if (_globallisteners[globalListenerIndex].count > 0) {
						_globallisteners[globalListenerIndex].count = _globallisteners[globalListenerIndex].count - 1;
					}
				}
			}
		};
	};
})(powerplayer);
/**
 * Plugin package definition
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	var _plugins = {};		
	var _pluginLoaders = {};
	
	powerplayer.plugins = function() {
	}
	
	powerplayer.plugins.loadPlugins = function(id, config) {
		_pluginLoaders[id] = new powerplayer.plugins.pluginloader(new powerplayer.plugins.model(_plugins), config);
		return _pluginLoaders[id];
	}
	
	powerplayer.plugins.registerPlugin = function(id, arg1, arg2) {
		var pluginId = powerplayer.utils.getPluginName(id);
		if (_plugins[pluginId]) {
			_plugins[pluginId].registerPlugin(id, arg1, arg2);
		} else {
			powerplayer.utils.log("A plugin ("+id+") was registered with the player that was not loaded. Please check your configuration.");
			for (var pluginloader in _pluginLoaders){
				_pluginLoaders[pluginloader].pluginFailed();
			}
		}
	}
})(powerplayer);
/**
 * Model that manages all plugins
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	powerplayer.plugins.model = function(plugins) {
		this.addPlugin = function(url) {
			var pluginName = powerplayer.utils.getPluginName(url);
			if (!plugins[pluginName]) {
				plugins[pluginName] = new powerplayer.plugins.plugin(url);
			}
			return plugins[pluginName];
		}
	}
})(powerplayer);
/**
 * Internal plugin model
 * @author zach
 * @version 5.8
 */
(function(powerplayer) {
	powerplayer.plugins.pluginmodes = {
		FLASH: "FLASH",
		JAVASCRIPT: "JAVASCRIPT",
		HYBRID: "HYBRID"
	}
	
	powerplayer.plugins.plugin = function(url) {
		var _repo = "http://plugins.longtailvideo.com"
		var _status = powerplayer.utils.loaderstatus.NEW;
		var _flashPath;
		var _js;
		var _completeTimeout;
		
		var _eventDispatcher = new powerplayer.events.eventdispatcher();
		powerplayer.utils.extend(this, _eventDispatcher);
		
		function getJSPath() {
			switch (powerplayer.utils.getPluginPathType(url)) {
				case powerplayer.utils.pluginPathType.ABSOLUTE:
					return url;
				case powerplayer.utils.pluginPathType.RELATIVE:
					return powerplayer.utils.getAbsolutePath(url, window.location.href);
				case powerplayer.utils.pluginPathType.CDN:
					var pluginName = powerplayer.utils.getPluginName(url);
					var pluginVersion = powerplayer.utils.getPluginVersion(url);
					var repo = (window.location.href.indexOf("https://") == 0) ? _repo.replace("http://", "https://secure") : _repo;
					return repo + "/" + powerplayer.version.split(".")[0] + "/" + pluginName + "/"
							+ pluginName + (pluginVersion !== "" ? ("-" + pluginVersion) : "") + ".js";
			}
		}
		
		function completeHandler(evt) {
			_completeTimeout = setTimeout(function(){
				_status = powerplayer.utils.loaderstatus.COMPLETE;
				_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
			}, 1000);
		}
		
		function errorHandler(evt) {
			_status = powerplayer.utils.loaderstatus.ERROR;
			_eventDispatcher.sendEvent(powerplayer.events.ERROR);
		}
		
		this.load = function() {
			if (_status == powerplayer.utils.loaderstatus.NEW) {
				if (url.lastIndexOf(".swf") > 0) {
					_flashPath = url;
					_status = powerplayer.utils.loaderstatus.COMPLETE;
					_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
					return;
				}
				_status = powerplayer.utils.loaderstatus.LOADING;
				var _loader = new powerplayer.utils.scriptloader(getJSPath());
				// Complete doesn't matter - we're waiting for registerPlugin 
				_loader.addEventListener(powerplayer.events.COMPLETE, completeHandler);
				_loader.addEventListener(powerplayer.events.ERROR, errorHandler);
				_loader.load();
			}
		}
		
		this.registerPlugin = function(id, arg1, arg2) {
			if (_completeTimeout){
				clearTimeout(_completeTimeout);
				_completeTimeout = undefined;
			}
			if (arg1 && arg2) {
				_flashPath = arg2;
				_js = arg1;
			} else if (typeof arg1 == "string") {
				_flashPath = arg1;
			} else if (typeof arg1 == "function") {
				_js = arg1;
			} else if (!arg1 && !arg2) {
				_flashPath = id;
			}
			_status = powerplayer.utils.loaderstatus.COMPLETE;
			_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
		}
		
		this.getStatus = function() {
			return _status;
		}
		
		this.getPluginName = function() {
			return powerplayer.utils.getPluginName(url);
		}
		
		this.getFlashPath = function() {
			if (_flashPath) {
				switch (powerplayer.utils.getPluginPathType(_flashPath)) {
					case powerplayer.utils.pluginPathType.ABSOLUTE:
						return _flashPath;
					case powerplayer.utils.pluginPathType.RELATIVE:
						if (url.lastIndexOf(".swf") > 0) {
							return powerplayer.utils.getAbsolutePath(_flashPath, window.location.href);
						}
						return powerplayer.utils.getAbsolutePath(_flashPath, getJSPath());
					case powerplayer.utils.pluginPathType.CDN:
						if (_flashPath.indexOf("-") > -1){
							return _flashPath+"h";
						}
						return _flashPath+"-h";
				}
			}
			return null;
		}
		
		this.getJS = function() {
			return _js;
		}

		this.getPluginmode = function() {
			if (typeof _flashPath != "undefined"
			 && typeof _js != "undefined") {
			 	return powerplayer.plugins.pluginmodes.HYBRID;
			 } else if (typeof _flashPath != "undefined") {
			 	return powerplayer.plugins.pluginmodes.FLASH;
			 } else if (typeof _js != "undefined") {
			 	return powerplayer.plugins.pluginmodes.JAVASCRIPT;
			 }
		}
		
		this.getNewInstance = function(api, config, div) {
			return new _js(api, config, div);
		}
		
		this.getURL = function() {
			return url;
		}
	}
	
})(powerplayer);
/**
 * Loads plugins for a player
 * @author zach
 * @version 5.6
 */
(function(powerplayer) {

	powerplayer.plugins.pluginloader = function(model, config) {
		var _plugins = {};
		var _status = powerplayer.utils.loaderstatus.NEW;
		var _loading = false;
		var _iscomplete = false;
		var _eventDispatcher = new powerplayer.events.eventdispatcher();
		powerplayer.utils.extend(this, _eventDispatcher);
		
		/*
		 * Plugins can be loaded by multiple players on the page, but all of them use
		 * the same plugin model singleton. This creates a race condition because
		 * multiple players are creating and triggering loads, which could complete
		 * at any time. We could have some really complicated logic that deals with
		 * this by checking the status when it's created and / or having the loader
		 * redispatch its current status on load(). Rather than do this, we just check
		 * for completion after all of the plugins have been created. If all plugins
		 * have been loaded by the time checkComplete is called, then the loader is
		 * done and we fire the complete event. If there are new loads, they will
		 * arrive later, retriggering the completeness check and triggering a complete
		 * to fire, if necessary.
		 */
		function _complete() {
			if (!_iscomplete) {
				_iscomplete = true;
				_status = powerplayer.utils.loaderstatus.COMPLETE;
				_eventDispatcher.sendEvent(powerplayer.events.COMPLETE);
			}
		}
		
		// This is not entirely efficient, but it's simple
		function _checkComplete() {
			if (!_iscomplete) {
				var incomplete = 0;
				for (plugin in _plugins) {
					var status = _plugins[plugin].getStatus(); 
					if (status == powerplayer.utils.loaderstatus.LOADING
							|| status == powerplayer.utils.loaderstatus.NEW) {
						incomplete++;
					}
				}
				
				if (incomplete == 0) {
					_complete();
				}
			}
		}
		
		this.setupPlugins = function(api, config, resizer) {
			var flashPlugins = {
				length: 0,
				plugins: {}
			};
			var jsplugins = {
				length: 0,
				plugins: {}
			};
			for (var plugin in _plugins) {
				var pluginName = _plugins[plugin].getPluginName();
				if (_plugins[plugin].getFlashPath()) {
					flashPlugins.plugins[_plugins[plugin].getFlashPath()] = config.plugins[plugin];
					flashPlugins.plugins[_plugins[plugin].getFlashPath()].pluginmode = _plugins[plugin].getPluginmode();
					flashPlugins.length++;
				}
				if (_plugins[plugin].getJS()) {
					var div = document.createElement("div");
					div.id = api.id + "_" + pluginName;
					div.style.position = "absolute";
					div.style.zIndex = jsplugins.length + 10;
					jsplugins.plugins[pluginName] = _plugins[plugin].getNewInstance(api, config.plugins[plugin], div);
					jsplugins.length++;
					if (typeof jsplugins.plugins[pluginName].resize != "undefined") {
						api.onReady(resizer(jsplugins.plugins[pluginName], div, true));
						api.onResize(resizer(jsplugins.plugins[pluginName], div));
					}
				}
			}
			
			api.plugins = jsplugins.plugins;
			
			return flashPlugins;
		};
		
		this.load = function() {
			_status = powerplayer.utils.loaderstatus.LOADING;
			_loading = true;
			
			/** First pass to create the plugins and add listeners **/
			for (var plugin in config) {
				if (powerplayer.utils.exists(plugin)) {
					_plugins[plugin] = model.addPlugin(plugin);
					_plugins[plugin].addEventListener(powerplayer.events.COMPLETE, _checkComplete);
					_plugins[plugin].addEventListener(powerplayer.events.ERROR, _checkComplete);
				}
			}
			
			/** Second pass to actually load the plugins **/
			for (plugin in _plugins) {
				// Plugin object ensures that it's only loaded once
				_plugins[plugin].load();
			}
			
			_loading = false;
			
			// Make sure we're not hanging around waiting for plugins that already finished loading
			_checkComplete();
		}
		
		this.pluginFailed = function() {
			_complete();
		}
		
		this.getStatus = function() {
			return _status;
		}
		
	}
})(powerplayer);
/**
 * Core component of the JW Player (initialization, API).
 *
 * @author zach
 * @version 5.4
 */
(function(powerplayer) {
	powerplayer.html5 = function(container) {
		var _container = container;
		
		this.setup = function(options) {
			powerplayer.utils.extend(this, new powerplayer.html5.api(_container, options));
			return this;
		};
		
		return this;
	};
})(powerplayer);

/** 
 * A factory for API calls that either set listeners or return data
 *
 * @author zach
 * @version 5.9
 */
(function(powerplayer) {

	powerplayer.html5.api = function(container, options) {
        var _status = powerplayer.utils.loaderstatus.NEW;
        var _js;
        var _completeTimeout;
        var _baseUrl = powerplayer.baseUrl;
        if(options.baseUrl !== undefined && options.baseUrl !== '') {
            _baseUrl = options.baseUrl;
        }
        var _url = 'powerplayer.html5.js';//options.src;
        var player;
        var _completedTask = 0;
        var _runningTask =0;
        var _bitrates = [];
        var _ads = {};
        var _markers = [];
        var _thumbs = [];
        var _state = powerplayer.api.events.state.IDLE;
        var _debug = (powerplayer.utils.exists(options.debug) && (options.debug.toString().toLowerCase() == 'console'));

        var _eventDispatcher = new powerplayer.html5.eventdispatcher(container.id, _debug);
        powerplayer.utils.extend(this, _eventDispatcher);

		var _api = {};
        var _logo;
        var _coment;
        var _danmuConfig = {};
        var _cm = null;
        var _socket = null;
        var _sources = [];
        var _backupservers = [];
        var _video360 = false;

		// var _container = document.createElement('div');
		// container.parentNode.replaceChild(_container, container);
		// _container.id = container.id;

        if(options.backupservers != '' && options.backupservers != undefined) {
            var strback = options.backupservers;
            if ( strback.lastIndexOf( "," ) == ( strback.length - 1 ))
                strback = strback.substring( 0, strback.length - 1 );
            var servers = strback.split(",");
            for (var i = 0; i < servers.length; i++ ) {
                _backupservers.push(servers[i]);
            }
        }

        if(options.video360 != '' && options.video360 != undefined) {
            _video360 = options.video360;
        }
		_api.version = powerplayer.version;
		_api.id = container.id;

        setupLoadingElements();
		if(options.weburlparam != '' && options.weburlparam != undefined && options.playbacktype != 'LIVE') {
            //加载多清晰度
            loadbitrates();

            //加载广告
            loadads();

            //标记
            loadmarkers();

            //加载缩略图
            if(options.thumbsbif === undefined || options.thumbsbif === '' ) {
                if(options.showthumbnails === undefined || options.showthumbnails === true) {
                    loadthumbnails();
                }
            }
        }
        //
        if(options.bulletscreen != '' && options.bulletscreen != undefined && options.bulletscreen) {
            loadDanmuConfig();
            //加载弹幕样式
            // loadCCLCSS();
            //加载弹幕库
            loadCCL();
        }

        if (options.powerdrmurl && options.powerdrmurl != undefined && options.powerdrmurl != '') {
            loadPowerDrm();
        }

        console.log('loading powerplayer.html5.js');
        //加载播放器
        loadh5();

		// var _model = new powerplayer.html5.model(_api, _container, options);
		// var _view = new powerplayer.html5.view(_api, _container, _model);
		// var _controller = new powerplayer.html5.controller(_api, _container, _model, _view);
		
		// _api.skin = new powerplayer.html5.skin();
		
		_api.pwPlay = function(state) {
			//if (typeof state == undefined || state === undefined) {
			//	_togglePlay();
			//} else if (state.toString().toLowerCase() == "true") {
                player.play();
			//} else {
            //    player.pause();
			//}
		};
		_api.pwPause = function(state) {
			//if (typeof state == undefined || state === undefined) {
			//	_togglePlay();
			//} else if (state.toString().toLowerCase() == "true") {
				player.pause();
			//} else {
			//	player.play();
			//}
		};
		function _togglePlay() {
			// if (_model.state == powerplayer.api.events.state.PLAYING || _model.state == powerplayer.api.events.state.BUFFERING) {
				// _controller.pause();
			// } else {
				// _controller.play();
			// }
		}
        function setupLoadingElements() {
            _logo = document.createElement("img");
            _logo.id = container.id + "_Loading";
            _logo.style.display = "block";
            _logo.style.position = "absolute";
            _logo.style.top = '50%';
            _logo.style.left = '50%';
            _logo.style.transform = 'translate(-50%,-50%)';
            _logo.src = _baseUrl + '/loading.gif';
            container.appendChild(_logo);
        }
        function destroyLoadingElements() {
            _logo.style.display = "none";
            // powerplayer.utils.setOuterHTML(_logo, '');
        }

        function getJSPath() {
            switch (powerplayer.utils.getPluginPathType(_url)) {
                case powerplayer.utils.pluginPathType.ABSOLUTE:
                    return _url;
                case powerplayer.utils.pluginPathType.RELATIVE:
                    return _baseUrl + '/' + _url;
                case powerplayer.utils.pluginPathType.CDN:
                    var pluginName = powerplayer.utils.getPluginName(_url);
                    var pluginVersion = powerplayer.utils.getPluginVersion(_url);
                    var repo = (window.location.href.indexOf("https://") == 0) ? _repo.replace("http://", "https://secure") : _repo;
                    return repo + "/" + powerplayer.version.split(".")[0] + "/" + pluginName + "/"
                        + pluginName + (pluginVersion !== "" ? ("-" + pluginVersion) : "") + ".js";
            }
        }

        function setupPlayer() {
            console.log('begin setup html5 player...')

            _status = powerplayer.utils.loaderstatus.COMPLETE;

            // _coment = document.createElement("div");
            // _coment.id = container.id + "_comment";
            // _coment.className = 'comment_container';
            // container.appendChild(_coment);

            console.log('initialize markers.')
            var markers = [];
            for ( var i = 0; i < _markers.length; i++ ) {
                var mark = _markers[i];
                markers.push(new pwplayer.Markers.StandardMarker(mark.time, mark.info));
            }

            console.log('initialize thumbsbif.')
            if(options.thumbsbif != undefined && options.thumbsbif != '' && (options.showthumbnails === undefined || options.showthumbnails === true)) {
                var spriteSheetUrl = options.thumbsbif;

                var numThumbs = 100;
                if(options.duration != '' && options.duration != undefined) {
                    var duration = parseInt(options.duration);
                    numThumbs = Math.floor(duration/10);
                }
                var thumbWidth = 160;
                var thumbHeight = 90;
                var numColumns = 10;
                var timeInterval = 10;

                console.log('initialize _thumbs.')
                _thumbs = pwplayer.Thumbnails.buildSpriteConfig(spriteSheetUrl, numThumbs, thumbWidth, thumbHeight, numColumns, timeInterval);
            }
            if(_sources.length == 0) {
                _sources.push(options.file);
            }
            if(options.levels != undefined && options.levels != '' && _bitrates.length == 0) {
                var levels = options.levels;
                for(i = 0; i < levels.length; i++) {
                    var bitrate = {};
                    bitrate.label = levels[i].height + 'p';
                    bitrate.src = levels[i].file;
                    _bitrates.push(bitrate);
                }
            }
            var vid_width = '100%';
            if(options.width !== undefined && options.width !== '') {
                vid_width = options.width;
            }
            var vid_height = '100%';
            if(options.height !== undefined && options.height !== '') {
                vid_height = options.height;
            }
            var hideControl = true;
            var chromeless = false;
            var allowUserInteraction = true;
            if(options.components.controlbar !== undefined && options.components.controlbar.position !== undefined) {
                if (options.components.controlbar.position === 'bottom') {
                    hideControl = false;
                } else if (options.components.controlbar.position === 'none') {
                    chromeless = true;
                }
            }
            if(options.allowUserInteraction !== undefined && options.allowUserInteraction === false) {
                allowUserInteraction = false;
            }
            var fileext = powerplayer.utils.extension(options.file);
            if(fileext === 'mp3') {
                hideControl = false;
            }
            var watermark = '';
            var watermarkpos = 'top-right';
            if(options.components.logo !== undefined && options.components.logo.file !== undefined ) {
                watermark = options.components.logo.file;
            }
            if(options.components.logo !== undefined && options.components.logo.position !== undefined ) {
                watermarkpos = options.components.logo.position;
            }
            var poster = '';
            if(options.image !== undefined && options.image !== '') {
                poster = options.image;
            }
            // 是否循环播放
            var loopplay = false;
            if(options.repeat !== undefined && options.repeat === 'always') {
                loopplay = true;
            }
            // 无缝切换
            var seamless = true;//(options.playbacktype !== undefined && options.playbacktype !== 'LIVE') ||
            if(options.seamless !== undefined && options.seamless === false) {
                seamless = false;
            }
            var playinline = true;
            if(options.playinline !== undefined && options.playinline === false) {
                playinline = false;
            }
            var isLive = false;
            if(options.playbacktype !== undefined && options.playbacktype === 'LIVE') {
                isLive = true;
            }
            // 是否禁用拖拽
            var seekdisabled = false;
            if(options.seekdisabled !== undefined && options.seekdisabled === true) {
                seekdisabled = true;
            }
			var fullscreendisabled = false;
            if(options.fullscreendisabled !== undefined && options.fullscreendisabled === true) {
                fullscreendisabled = true;
            }
            var noactiontime = 0;
            if(options.noactiontime !== undefined && isNaN(options.noactiontime) === false) {
                noactiontime = parseInt(options.noactiontime);
            }
            var preview = false;
            if(options.preview !== undefined && options.preview === true) {
                preview = true;
            }
            var prviewTime = 300;
            if(options.prviewTime !== undefined && isNaN(options.prviewTime) === false) {
                prviewTime = parseInt(options.prviewTime);
            }
            var headtime = 0;
            if(options.headtime !== undefined && isNaN(options.headtime) === false) {
                headtime = parseInt(options.headtime);
            }
            var bottomtime = 0;
            if(options.bottomtime !== undefined && isNaN(options.bottomtime) === false) {
                bottomtime = parseInt(options.bottomtime);
            }
            var skipheadbottom = true;
            if(options.skipheadbottom !== undefined && options.skipheadbottom === false) {
                skipheadbottom = false;
            }
            var subtitlesrc = '';
            if(options.components.captions !== undefined && options.components.captions.file !== undefined ) {
                subtitlesrc = options.components.captions.file;
            } else {
                subtitlesrc = (options.subtitle === undefined) ? '' : options.subtitle;
            }
            var flvhasaudio = undefined;
            if(options.hasAudio !== undefined && typeof options.hasAudio === 'boolean') {
                flvhasaudio = options.hasAudio;
            }
            var flvhasvideo = undefined;
            if(options.hasVideo !== undefined && typeof options.hasVideo === 'boolean') {
                flvhasvideo = options.hasVideo;
            }
            var pipenabled = false;
            if(options.pip !== undefined && typeof options.pip === 'boolean') {
                pipenabled = options.pip;
            }
            var screenshot = false;
            if(options.screenshot !== undefined && typeof options.screenshot === 'boolean') {
                screenshot = options.screenshot;
            }
            console.log('embed html5 player...')
            player = new pwplayer.Player({
                sources: _sources,
                title: options.title,
                autoPlay: options.autostart,
                baseUrl:_baseUrl,
                width: vid_width,
                height: vid_height,
                watermark: watermark,
                position: watermarkpos,
                poster: poster,
                hideMediaControl: hideControl,
                chromeless: chromeless,
                allowUserInteraction: allowUserInteraction,
                loop: loopplay,
                seekdisabled: seekdisabled,
				fullscreendisabled: fullscreendisabled,
                noactiontime: noactiontime,
                preview: preview,
                prviewTime: prviewTime,
                skipheadbottom: skipheadbottom,
                headtime: headtime,
                bottomtime: bottomtime,
                bulletscreen: options.bulletscreen,
                provider: options.provider,
                clientcert: options.clientcert,
                powerdrmurl: options.powerdrmurl,
                latencythreshold: options.latencythreshold,
				mute: options.mute,
                screenshot: screenshot,
                playback: {
                    flvjsConfig: {
                        // Params from flv.js
                        enableLogging: true,
                        enableWorker: false,
                        lazyLoadMaxDuration: 3 * 60,
                        seekType: 'range',
                        isLive: isLive,
                        hasVideo: flvhasvideo,
                        hasAudio: flvhasaudio
                    },
                    hlsjsConfig: {
                        baseurl: options.weburlparam,
                        liveSyncDurationCount: 3 // To have at least 7 segments in queue
                    },
                    hlsUseNextLevel: seamless,
                    playInline: playinline
                },
                playbacktype: options.playbacktype,
                playbackRateConfig: {
                    defaultValue: '1.0',
                    options: [
                        {value: '0.5', label: '0.5X'},
                        {value: '0.75', label: '0.75X'},
                        {value: '1.0', label: '1.0X'},
                        {value: '1.5', label: '1.5X'},
                        {value: '2.0', label: '2.0X'},
                    ]
                },
                levelSelectorConfig: {
                    title: '',
                    labels: {
                        6: '原画', // 8000kbps
                        5: '4K',  // 6000kbps
                        4: '2K',  // 3500kbps
                        3: '超清', // 3000kbps
                        2: '高清', // 1500kbps
                        1: '标清', // 900kbps
                        0: '流畅'  // 400kbps
                    },
                    labelCallback: function(playbackLevel, customLabel) {
                        if(playbackLevel.level === undefined || playbackLevel.level.height === undefined) {
                            return customLabel;
                        }
                        return customLabel + playbackLevel.level.height+'p'; // High 720p
                    }
                },
                markers: {
                    markers: markers,
                    tooltipBottomMargin: 17
                },
                thumbnails: {
                    backdropHeight: 64, // set to 0 or null to disable backdrop
                    spotlightHeight: 84, // set to 0 or null to disable spotlight
                    thumbs: _thumbs
                },
                bitrateSelectorConfig: {
                    bitrates: _bitrates
                },
                ads: _ads,
                lastplayposition: options.lastplayposition,
                powerStats: {
                    onCompletion: [5, 20, 50, 70, 100],
                    shortcut: ['command+shift+s', 'ctrl+shift+s'],
                    iconPosition: 'top-right',
                    baseurl: options.weburlparam,
                    reportserver: options.statisticsserver,
                    streamid: options.streamid,
                    contentid: options.contentid,
                    videoname: options.title,
                    videoid: options.fileid,
                    siteid: options.siteid,
                    sourceid: options.sourceid,
                    username: options.username
                },
                subtitle : {
                    src : subtitlesrc,
                    auto : true,
                    backgroundColor : 'transparent',
                    fontWeight : 'normal',
                    fontSize : '20px',
                    color: '#fff',
                    textShadow : '1px 1px #000'
                },
                iceservers : options.iceservers,
                wstrackers : options.wstrackers,
                events: {
                    onReady: onReady,
                    onPlay: onPlay,
                    onPause: onPause,
                    onStop: onStop,
                    onError: onError,
                    onSeek: onSeek,
                    onEnded: onEnded,
                    onTimeUpdate: onTimeUpdate,
                    onFullscreen: onFullscreen,
                    onSwitchDanmu: onSwitchDanmu,
                    onSendDanmu: onSendDanmu,
                    onMeta: onMeta,
                },
                mirrorServers : _backupservers,
                video360 : _video360,
                adpad: options.adpad,
                liveopened: options.liveopened,
                marquees: options.marquees,
                backtosee: options.backtosee,
                gettimeurl: options.gettimeurl,
                mimeType: options.mimeType,
                useDvrControls: options.useDvrControls,
                pip: pipenabled,
                fullscreenWeb: options.fullscreenWeb
            });
            player.attachTo(container);
            console.log('html5 player created.')
        }

        function _sendEvent(type, obj) {
            if (obj) {
                _eventDispatcher.sendEvent(type, obj);
            } else {
                _eventDispatcher.sendEvent(type);
            }
        }

        function onReady() {
            destroyLoadingElements();
            if(_danmuConfig && _danmuConfig.load && (typeof CommentManager != 'undefined')) {
                //得到弹幕层
                _coment = document.getElementsByClassName('pwplayer-comment');
                //父节点
                // var eleParent = _coment[0].parentNode;
                // if(eleParent) {
                    container.className = container.className + ' comment-parent';
                // }
                //替换{$id}
                var loadurl = _danmuConfig.load.replace('{$id}', options.fileid);//'http://115.28.215.145:8080/powercms/service/DanMuService-showDanMu.action?cid=f39c5711636e36130163710ba61301ea&r=407';//_danmuConfig.load.replace('{$id}', options.fileid);
                _cm = new CommentManager(_coment[0]);
                if (options.danmuscale) {
                    _cm.options.global.scale = parseFloat(options.danmuscale)
                }
                _cm.init();
                _cm.start();
                if (window && typeof window.addEventListener != "undefined") {
                    window.addEventListener("resize", function(){
                        //Notify on resize
                        _cm.setBounds();
                    });
                }
                var cp = (new CommentProvider()).addStaticSource(
                    CommentProvider.XMLProvider("GET", loadurl),
                    CommentProvider.SOURCE_XML).addParser(
                    new CommonDanmakuFormat.XMLParser(),
                    CommentProvider.SOURCE_XML).addTarget(_cm);
                cp.start().catch(function (e) {
                    //alert(e);
                });
                _cm.clear();
            }
            //加载实时弹幕
            if(_danmuConfig && _danmuConfig.wsserver) {
                loadSocket();
            }
            if (powerplayer.utils.exists(window.playerReady)) {
                var evt = {
                    id: _api.id,
                    version: _api.version
                };
                playerReady(evt);
            }
            _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_LOADED);
        }

        function onPlay() {
            if(_cm)
                _cm.start();
            _setState(powerplayer.api.events.state.PLAYING);
        }
        function onPause() {
            if(_cm)
                _cm.stop();
            _setState(powerplayer.api.events.state.PAUSED);
        }
        function onStop() {
            if(_cm)
                _cm.stop();
            if(_socket) {
                _socket.emit('leave');
            }
            _setState(powerplayer.api.events.state.IDLE);
        }
        function onSeek(time) {
            _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_SEEK, {position: time});
        }
        function onError(error) {
            // console.log('onError: code = ' + error.code + ', desc = ' + error.description);
            if(_cm)
                _cm.stop();
            if(_socket) {
                _socket.emit('leave');
            }
            // _sendEvent(powerplayer.api.events.POWERPLAYER_ERROR, {
            //     code: error.error.code,
            //     message: error.error.message
            // });
            _sendEvent(powerplayer.api.events.POWERPLAYER_ERROR, error.error);
        }
        function onEnded(name) {
            // console.log('onEnded: ' + name);
            if(_cm)
                _cm.stop();
            if(_socket) {
                _socket.emit('leave');
            }
            _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_COMPLETE);
        }
        function onTimeUpdate(event) {
            // console.log('onTimeUpdate: position = ' +  event.current + '/' + event.total);
            if(_cm)
                _cm.time(Math.round(event.current*1000));
            if (event.seektime !== undefined) {
                _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_TIME, {
                    position: event.current,
                    duration: event.total,
                    seektime: event.seektime
                });
            } else {
                _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_TIME, {
                    position: event.current,
                    duration: event.total
                });
            }
        }
        function onFullscreen(fullscreen) {
            if(_cm) {
                _cm.setBounds();
            }
            _sendEvent(powerplayer.api.events.POWERPLAYER_FULLSCREEN, fullscreen);
        }
        function onSwitchDanmu(status) {
            if(_cm ) {
                if(status) {
                    _cm.display = false;
                    _cm.start();
                } else {
                    _cm.display = false;
                    _cm.clear();
                    _cm.stop();
                }
            }
        }
        function onSendDanmu(toSend) {
            if(_cm && player) {
                var comment = {
                    "mode":1,
                    "text": toSend.text,
                    "stime": player.getCurrentTime() * 1000 - 1,
                    "size":toSend.size,
                    "color":toSend.color,
                    'border': true
                };
                if(options.playbacktype != undefined && options.playbacktype === 'LIVE')  {
                    comment.stime = -1;
                }
                _cm.insert(comment);
                _cm.send(comment);
                sendCommentToServer(comment);
                //websocket实时弹幕
                if(_socket) {
                    _socket.emit('danmaku', JSON.stringify(comment));
                }
            }
        }
        function onMeta(metadata) {
            _sendEvent(powerplayer.api.events.POWERPLAYER_MEDIA_META, metadata); 
        }
        function _setState(newstate) {
            // Handles FF 3.5 issue
            if (newstate == powerplayer.api.events.state.PAUSED && _state == powerplayer.api.events.state.IDLE) {
                return;
            }

            if (_state != newstate) {
                var oldstate = _state;
                _state = newstate;
                _sendEvent(powerplayer.api.events.POWERPLAYER_PLAYER_STATE, {
                    oldstate: oldstate,
                    newstate: newstate
                });
            }
        }
        function completeHandler(evt) {
            _completedTask ++;
            console.log('powerplayer.html5.js loaded, completed = ' + _completedTask);
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function errorHandler(evt) {
            _status = powerplayer.utils.loaderstatus.ERROR;
            console.log('failed load powerplayer.html5.js, status = ' + _status);
        }

        function loadh5() {
            if (_status == powerplayer.utils.loaderstatus.NEW) {
                _status = powerplayer.utils.loaderstatus.LOADING;
                var _loaderplayer = new powerplayer.utils.scriptloader(getJSPath());
                // Complete doesn't matter - we're waiting for registerPlugin
                _loaderplayer.addEventListener(powerplayer.events.COMPLETE, completeHandler);
                _loaderplayer.addEventListener(powerplayer.events.ERROR, errorHandler);
                _loaderplayer.load();
                _runningTask ++;
            }
        }

        //socket.io.js加载完成
        function socketCompleteHandler(evt) {
            _socket = io(_danmuConfig.wsserver);

            _socket.on('connect', function () {
                _socket.emit('join', options.fileid);
            });

            _socket.on('danmaku', function(data){
                // 当遇到 danmaku 事件，就把推送来的弹幕推送给 CCL
                try {
                    var danmaku = JSON.parse(data);
                    if (danmaku.hasOwnProperty('stime') && danmaku.stime > 0) {
                        // 弹幕有时间轴位置，那就插入时间轴
                        _cm.insert(danmaku);
                    } else {
                        // 弹幕没有时间轴位置就立刻显示（不记录）
                        _cm.send(danmaku);
                    }
                } catch(e) {
                    console.log('error in insert realtime danmu');
                }
            });
        }

        function socketErrorHandler(evt) {
		    console.log('Failed to load socket.io.js')
        }

        function loadSocket() {
            var loadSocket = new powerplayer.utils.scriptloader(_baseUrl + '/socket.io.js');
            loadSocket.addEventListener(powerplayer.events.COMPLETE, socketCompleteHandler);
            loadSocket.addEventListener(powerplayer.events.ERROR, socketErrorHandler);
            loadSocket.load();
        }

        function cclCompleteHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function cclErrorHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadCCL() {
            var loadCCL = new powerplayer.utils.scriptloader(_baseUrl + '/ccl.js');
            // Complete doesn't matter - we're waiting for registerPlugin
            loadCCL.addEventListener(powerplayer.events.COMPLETE, cclCompleteHandler);
            loadCCL.addEventListener(powerplayer.events.ERROR, cclErrorHandler);
            loadCCL.load();
            _runningTask ++;
        }

        function drmCompleteHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function drmErrorHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadPowerDrm() {
            var loadDRM = new powerplayer.utils.scriptloader(_baseUrl + '/powerdrm.js');
            // Complete doesn't matter - we're waiting for registerPlugin
            loadDRM.addEventListener(powerplayer.events.COMPLETE, drmCompleteHandler);
            loadDRM.addEventListener(powerplayer.events.ERROR, drmErrorHandler);
            loadDRM.load();
            _runningTask ++;
        }

        function cclcssCompleteHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function cclcssErrorHandler(evt) {
            _completedTask ++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadCCLCSS() {
            var loadCCLCSS = new powerplayer.utils.linkloader(_baseUrl + '/ccl.css');
            // Complete doesn't matter - we're waiting for registerPlugin
            loadCCLCSS.addEventListener(powerplayer.events.COMPLETE, cclcssCompleteHandler);
            loadCCLCSS.addEventListener(powerplayer.events.ERROR, cclcssErrorHandler);
            loadCCLCSS.load();
            _runningTask ++;
        }

        //加载弹幕库配置
        function loadDanmuConfig() {
            var resturl = _baseUrl + '/config.xml';
            powerplayer.utils.ajax(resturl, loadDanmuConfig_succ, loadDanmuConfig_fail, 'xml');
            _runningTask ++;
        }

        function loadDanmuConfig_succ(xmlhttp) {
            _completedTask ++;
            _danmuConfig = {};
            try {
                var root = xmlhttp.responseXML;
                var server = root.getElementsByTagName("server");
                var load = server[0].getElementsByTagName("load");
                if(load) {
                    _danmuConfig.load = load[0].childNodes[0].nodeValue;
                }
                var send = server[0].getElementsByTagName("send");
                if(send) {
                    _danmuConfig.send = send[0].childNodes[0].nodeValue;
                }
                var wsserver = server[0].getElementsByTagName("websocket");
                if(wsserver) {
                    _danmuConfig.wsserver = wsserver[0].childNodes[0].nodeValue;
                }
            } catch (e) {
                console.log('Failed to load danmu config');
            }

            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadDanmuConfig_fail(data) {
            _completedTask++;
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        //加载多码率
        function loadbitrates() {
            var resturl = options.weburlparam + '/service/flashPlayerService-getDefinitionInfo.action?fileId=' + options.fileid + '&contentId=' + options.contentid;
            powerplayer.utils.ajax(resturl, loadbitrates_succ, loadbitrates_fail, 'json');
            _runningTask ++;
        }

        function loadbitrates_succ(data) {
            _completedTask ++;
            console.log('definition loaded, completed = ' + _completedTask);
            var filePath = options.file;
            var fileproto = 'http://';
            if(filePath.indexOf('http://') == 0) {
                filePath = filePath.substring(7);
            } else if(filePath.indexOf('https://') == 0){
                filePath = filePath.substring(8);
                fileproto = 'https://';
            }

            var srcext = powerplayer.utils.extension(filePath);
            if(srcext == 'm3u8') {
                if(_completedTask >= _runningTask) {
                    setupPlayer();
                }
                return;
            }

            _bitrates = [];
            var slashpos = filePath.indexOf('/');
            var serveraddr = filePath.substring(0, slashpos);

            var colonpos = serveraddr.lastIndexOf(':');
            var playPort = '80';
            if(colonpos > 0) {
                playPort = serveraddr.substring(colonpos + 1);
            }
            var ismobile = powerplayer.utils.isMobile();
            var preferDefinition = powerplayer.utils.readCookie('definition');
            if(preferDefinition == undefined) {
                if(ismobile) {
                    if(data && data.smooth != undefined ) {
                        preferDefinition = 'ld';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }

                    if(preferDefinition == undefined && data && data.sd != undefined) {
                        preferDefinition = 'sd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.hd != undefined) {
                        preferDefinition = 'hd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.fhd != undefined) {
                        preferDefinition = 'fhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.qhd != undefined) {
                        preferDefinition = 'qhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.qhd != undefined) {
                        preferDefinition = 'uhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.od != undefined) {
                        preferDefinition = 'od';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                } else {
                    if(preferDefinition == undefined && data && data.hd != undefined) {
                        preferDefinition = 'hd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.sd != undefined) {
                        preferDefinition = 'sd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.smooth != undefined ) {
                        preferDefinition = 'ld';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.fhd != undefined) {
                        preferDefinition = 'fhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.qhd != undefined) {
                        preferDefinition = 'qhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.qhd != undefined) {
                        preferDefinition = 'uhd';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                    if(preferDefinition == undefined && data && data.od != undefined) {
                        preferDefinition = 'od';
                        // powerplayer.utils.saveCookie('definition', 'ld');
                    }
                }
            }

            if(data && data.smooth != undefined) {
                var bitrate = {};
                if(data.smooth.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.smooth.length; i < l; i++) {
                        var definitionpath = data.smooth[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            } else {
                                m3u8server = data.smooth[i].playserverip;
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            } else {
                                flvserver = data.smooth[i].playserverip;
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath;
                            mp4server = data.smooth[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                } else {
                    var serverip = data.smooth[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.smooth[0].path;
                }

                bitrate.label = '流畅';
                // if(data.smooth[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'ld' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.sd != undefined) {
                var bitrate = {};
                if(data.sd.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.sd.length; i < l; i++) {
                        var definitionpath = data.sd[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.sd[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.sd[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.sd[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                    _sources = sources;
                } else {
                    var serverip = data.sd[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.sd[0].path;
                }

                bitrate.label = '标清';
                // if(data.sd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'sd' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.hd != undefined) {
                var bitrate = {};
                if(data.hd.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.hd.length; i < l; i++) {
                        var definitionpath = data.hd[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.hd[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.hd[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.hd[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                    _sources = sources;
                } else {
                    var serverip = data.hd[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.hd[0].path;
                }

                bitrate.label = '高清';
                // if(data.hd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'hd' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.fhd != undefined) {
                var bitrate = {};
                if(data.fhd.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.fhd.length; i < l; i++) {
                        var definitionpath = data.fhd[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.fhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.fhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.fhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                    _sources = sources;
                } else {
                    var serverip = data.fhd[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.fhd[0].path;
                }

                bitrate.label = '超清';
                // if(data.hd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'fhd' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.qhd != undefined) {
                var bitrate = {};
                if(data.qhd.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.qhd.length; i < l; i++) {
                        var definitionpath = data.qhd[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.qhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.qhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.qhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                } else {
                    var serverip = data.qhd[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.qhd[0].path;
                }

                bitrate.label = '2K';
                // if(data.hd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'qhd' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.uhd != undefined) {
                var bitrate = {};
                if(data.uhd.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.uhd.length; i < l; i++) {
                        var definitionpath = data.uhd[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.uhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.uhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.uhd[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                } else {
                    var serverip = data.uhd[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.uhd[0].path;
                }

                bitrate.label = '4K';
                // if(data.hd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'uhd' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }
            if(data && data.od != undefined) {
                var bitrate = {};
                if(data.od.length > 1) {
                    var sources = [];
                    var m3u8path = ''; var m3u8server = '';
                    var flvpath = ''; var flvserver = '';
                    var mp4path = ''; var mp4server = '';
                    for (var i = 0, l = data.od.length; i < l; i++) {
                        var definitionpath = data.od[i].path;
                        var ext = powerplayer.utils.extension(definitionpath);
                        if(ext == 'm3u8') {
                            m3u8path = definitionpath;
                            m3u8server = data.od[i].playserverip;
                            if(_backupservers.length > 0) {
                                m3u8server = _backupservers[0];
                            }
                        } else if(ext == 'flv'){
                            flvpath = definitionpath;
                            flvserver = data.od[i].playserverip;
                            if(_backupservers.length > 0) {
                                flvserver = _backupservers[0];
                            }
                        } else if(ext == 'mp4'){
                            mp4path = definitionpath
                            mp4server = data.od[i].playserverip;
                            if(_backupservers.length > 0) {
                                mp4server = _backupservers[0];
                            }
                        }
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + m3u8server + ':' + playPort + '/' + m3u8path);
                    }
                    if(flvpath != '') {
                        sources.push(fileproto + flvserver + ':' + playPort + '/' + flvpath);
                    }
                    if(m3u8path != '') {
                        sources.push(fileproto + mp4server + ':' + playPort + '/' + mp4path);
                    }
                    bitrate.srcs = sources;
                } else {
                    var serverip = data.od[0].playserverip;
                    if(_backupservers.length > 0) {
                        serverip = _backupservers[0];
                    }
                    bitrate.src = fileproto + serverip + ':' + playPort + '/' + data.od[0].path;
                }

                bitrate.label = '原画';
                // if(data.hd[0].id === options.fileid) {
                //     bitrate.default = true;
                // }
                if(preferDefinition == 'od' ) {
                    bitrate.default = true;
                    if(bitrate.srcs != undefined) {
                        _sources = sources;
                    } else {
                        _sources.push(bitrate.src);
                    }
                }
                _bitrates.push(bitrate);
            }

            console.log('definition parsed');
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadbitrates_fail(path) {
            _completedTask++;
            console.log('failed load definition');
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        //加载广告
        function loadads() {
            var resturl = options.weburlparam + '/service/flashPlayerService-getAD.action?fileId=' + options.fileid + '&contentId=' + options.contentid;
            powerplayer.utils.ajax(resturl, loadads_succ, loadads_fail, 'json');
            _runningTask ++;
        }

        function loadads_succ(data) {
            _completedTask ++;
            console.log('ads loaded, completed = ' + _completedTask);
            if(data && data[0] && data[0].adpause != '' && data[0].adpause != undefined) {
                _ads.paused = {};
                _ads.paused.image = data[0].adpause;
                _ads.paused.imageLink = data[0].adpauseLink;
                _ads.paused.adId = data[0].adpauseId;
            }
            if(data && data[0] && data[0].adhead != '' && data[0].adhead != undefined) {
                _ads.preRoll = {};
                _ads.preRoll.src = data[0].adhead;
                _ads.preRoll.adId = data[0].adheadId;
                _ads.preRoll.skip = (options.skipad === undefined)?false:options.skipad;
                // if(data[0].adheadDuration === '' || data[0].adheadDuration === undefined) {
                    _ads.preRoll.timeout = 5;
                // } else {
                //     _ads.preRoll.timeout = 5;//parseInt(data[0].adheadDuration);
                // }
                _ads.text = {};
                _ads.text.wait = '$ | 你可以在%秒后关闭广告';
                _ads.text.skip = '$ | 关闭广告';
            }
            if(data && data[0] && data[0].adend != '' && data[0].adend != undefined) {
                _ads.postRoll = {};
                _ads.postRoll.src = data[0].adend;
                _ads.postRoll.adId = data[0].adendId;
            }

            console.log('ads data parsed');
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadads_fail(path) {
            _completedTask++;
            console.log('failed load ads ');
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        //加载标记
        function loadmarkers() {
            var resturl = options.weburlparam + '/service/flashPlayerService-getTimepoints.action?fileId=' + options.fileid + '&filePath=' + options.file;
            powerplayer.utils.ajax(resturl, loadmarkers_succ, loadmarkers_fail, 'json');
            _runningTask ++;
        }

        function loadmarkers_succ(data) {
            _completedTask ++;
            console.log('markers loaded, completed = ' + _completedTask);
            _markers = data;
            console.log('')
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadmarkers_fail(path) {
            _completedTask++;
            console.log('failed load markers ');
            if(_completedTask >= _runningTask) {
                setupPlayer();
            }
        }

        function loadthumbnails() {
            var filePath = options.file;
            if(filePath.indexOf('http://') == 0) {
                filePath = filePath.substring(7);
            } else if(filePath.indexOf('https://') == 0){
                filePath = filePath.substring(8);
            }
            var slashpos = filePath.indexOf('/');
            var serveraddr = filePath.substring(0, slashpos);
            var path = filePath.substring(slashpos + 1);

            var colonpos = serveraddr.lastIndexOf(':');
            var playIp = serveraddr;
            var playPort = '80';
            if(colonpos > 0) {
                playIp = serveraddr.substring(0, colonpos);
                // playPort = serveraddr.substring(colonpos + 1);
            }

            var duration = parseInt(options.duration);
            duration = Math.floor(duration/20);
            for (var i = 1; i < duration; i++) {
                var timepoint = i * 20;
                var resturl = options.weburlparam + '/service/flashPlayerService-interceptImage.action?fileId=' + options.fileid + '&filePath=' + path + '&playIp=' + playIp + '&timePoint=' + timepoint;
                _thumbs.push({
                    url: resturl,
                    time: timepoint
                });
            }
        }

		_api.pwStop = function() {
            if(player) {
                player.stop()
            }
        }
		_api.pwSeek = function(pos) {
            if(player) {
                player.seek(pos)
            }
        }
		// _api.pwPlaylistItem = function(item) {
		// 	if (_instreamPlayer) {
		// 		if (_instreamPlayer.playlistClickable()) {
		// 			_instreamPlayer.pwInstreamDestroy();
		// 			return _controller.item(item);
		// 		}
		// 	} else {
		// 		return _controller.item(item);
		// 	}
		// }
		// _api.pwPlaylistNext = _controller.next;
		// _api.pwPlaylistPrev = _controller.prev;
		_api.pwResize = function(width, height) {
            player.resize({height: height, width: width});
        };
		_api.pwLoad = function(source) {
            player.load(source)
        };
		// _api.pwDetachMedia = _controller.detachMedia;
		// _api.pwAttachMedia = _controller.attachMedia;
		
		function _statevarFactory(statevar) {
			return function() {
				// return _model[statevar];
			};
		}
		
		function _componentCommandFactory(componentName, funcName, args) {
			// return function() {
			// 	var comp = _model.plugins.object[componentName];
			// 	if (comp && comp[funcName] && typeof comp[funcName] == "function") {
			// 		comp[funcName].apply(comp, args);
			// 	}
			// };
		}
		
		_api.pwGetPlaylistIndex = _statevarFactory('item');
		_api.pwGetPosition = function() {
		    return player.getCurrentTime();
        }
		_api.pwGetDuration = function() {
            return player.getDuration();
        };
		_api.pwGetBuffer = _statevarFactory('buffer');
		_api.pwGetWidth = function() {
            return player.getWidth();
        };
		_api.pwGetHeight = function() {
            return player.getHeight();
        };
		_api.pwGetFullscreen = _statevarFactory('fullscreen');
		// _api.pwSetFullscreen = _controller.setFullscreen;
		// _api.pwGetVolume = _statevarFactory('volume');
		// _api.pwSetVolume = _controller.setVolume;
		_api.pwGetMute = function() {
            if (player.getVolume() == 0) {
                return true;
            } else {
                return false;
            }
        };
		_api.pwSetMute = function(mute) {
            if (mute) {
                player.mute()
            } else {
                player.unmute()
            }

        };
		_api.pwGetStretching = function() {
			// return _model.stretching.toUpperCase();
		}
		
		_api.pwGetState = function() {//_statevarFactory('state');
            if(player.isPlaying()) {
                return 'PLAYING';
            } else {
                return 'PAUSED';
            }
        }
		_api.pwGetVersion = function() {
			return _api.version;
		};
		_api.pwGetPlaylist = function() {
			// return _model.playlist;
		};
		
		_api.pwAddEventListener = this.addEventListener;
		_api.pwRemoveEventListener = this.removeEventListener;
		_api.pwSendEvent = this.sendEvent;
		
		_api.pwDockSetButton = function(id, handler, outGraphic, overGraphic) {
			// if (_model.plugins.object["dock"] && _model.plugins.object["dock"].setButton) {
			// 	_model.plugins.object["dock"].setButton(id, handler, outGraphic, overGraphic);
			// }
		}
		
		_api.pwControlbarShow = _componentCommandFactory("controlbar", "show");
		_api.pwControlbarHide = _componentCommandFactory("controlbar", "hide");
		_api.pwDockShow = _componentCommandFactory("dock", "show");
		_api.pwDockHide = _componentCommandFactory("dock", "hide");
		_api.pwDisplayShow = _componentCommandFactory("display", "show");
		_api.pwDisplayHide = _componentCommandFactory("display", "hide");

		var _instreamPlayer;
		
		//InStream API
		_api.pwLoadInstream = function(item, options) {
			if (!_instreamPlayer) {
				// _instreamPlayer = new powerplayer.html5.instream(_api, _model, _view, _controller);
			}
			setTimeout(function() {
				// _instreamPlayer.load(item, options);
			}, 10);
		}
		_api.pwInstreamDestroy = function() {
			// if (_instreamPlayer) {
			// 	_instreamPlayer.pwInstreamDestroy();
			// }
		}
		
		_api.pwInstreamAddEventListener = _callInstream('pwInstreamAddEventListener');
		_api.pwInstreamRemoveEventListener = _callInstream('pwInstreamRemoveEventListener');
		_api.pwInstreamGetState = _callInstream('pwInstreamGetState');
		_api.pwInstreamGetDuration = _callInstream('pwInstreamGetDuration');
		_api.pwInstreamGetPosition = _callInstream('pwInstreamGetPosition');
		_api.pwInstreamPlay = _callInstream('pwInstreamPlay');
		_api.pwInstreamPause = _callInstream('pwInstreamPause');
		_api.pwInstreamSeek = _callInstream('pwInstreamSeek');
		
		function _callInstream(funcName) {
			return function() {
				if (_instreamPlayer && typeof _instreamPlayer[funcName] == "function") {
					// return _instreamPlayer[funcName].apply(this, arguments);
				} else {
					_utils.log("Could not call instream method - instream API not initialized");
				}
			}
		}

		_api.pwDestroy = function() {
            player.destroy();
		}
		
		//UNIMPLEMENTED
		_api.pwGetLevel = function() {
		};
		_api.pwGetBandwidth = function() {
		};
		_api.pwGetLockState = function() {
		};
		_api.pwLock = function() {
		};
		_api.pwUnlock = function() {
		};
		_api.pwSetBulletscreen = function (bulletscreen) {
            if(_cm ) {
                if(bulletscreen) {
                    _cm.display = false;
                    _cm.start();
                } else {
                    _cm.display = false;
                    _cm.clear();
                    _cm.stop();
                }
            }
        }
		_api.pwSendBullet = function (toSend) {
            if(_cm && player) {
                var comment = {
                    "mode":1,
                    "text": toSend.text,
                    "stime": player.getCurrentTime() * 1000 - 1,
                    "size":toSend.size,
                    "color":toSend.color,
                    'border': true
                };
                if(options.playbacktype != undefined && options.playbacktype === 'LIVE')  {
                    comment.stime = -1;
                }
                _cm.insert(comment);
                _cm.send(comment);
                sendCommentToServer(comment);
                //websocket实时弹幕
                if(_socket) {
                    _socket.emit('danmaku', JSON.stringify(comment));
                }
            }
        }
        _api.pwOpenLive = function () {
            player.openLive();
        }
        _api.pwCloseLive = function () {
            player.closeLive();
        }
        _api.pwCutInAd = function (url, duration) {

        }
        _api.pwCutOutAd = function () {

        }
        _api.pwInsertMarquee = function (toInsert) {
            if(player) {
                player.insertMarquee(toInsert);
            }
        }
        _api.pwSnapshot = function () {
            if (player) {
                return player.snapshot();
            } else {
                reject('player is not running');
            }
        }
        _api.pwZoomVideo = function (params) {
            if (player) {
                return player.zoomVideo(params.winWidth, params.winHeight, params.selWidth, params.selHeight, params.centerX, params.centerY);
            } else {
                reject('player is not running');
            }
        }
        _api.pwResetZoom = function () {
            if (player) {
                return player.resetZoom();
            } else {
                reject('player is not running');
            }
        }
        function sendCommentToServer(comment) {
		    if(_danmuConfig.send) {
                var sendurl = _danmuConfig.send.replace('{$id}', options.fileid);
                var xhr = new XMLHttpRequest();
                xhr.open('POST', sendurl,
                    true);
                xhr.setRequestHeader('Content-type', 'application/x-www-form-urlencoded');
                xhr.onreadystatechange = function() {
                    console.log(xhr.responseText);
                    if (xhr.readyState == 4 && xhr.status == 200) {
                        console.log(xhr.responseText);
                    }
                };
                var playtime = parseInt(comment.stime)/1000;
                var content = 'user='+options.username+'&fileId='+options.fileid+'&cid='+options.fileid+'&message='+comment.text+'&size='+comment.size+'&color='+comment.color+'&stime='+playtime+'&mode='+comment.mode;
                xhr.send(content); // 发送请求
            }
        }
		function _skinLoaded() {
			// if (_model.config.playlistfile) {
			// 	_model.addEventListener(powerplayer.api.events.JWPLAYER_PLAYLIST_LOADED, _playlistLoaded);
			// 	_model.loadPlaylist(_model.config.playlistfile);
			// } else if (typeof _model.config.playlist == "string") {
			// 	_model.addEventListener(powerplayer.api.events.JWPLAYER_PLAYLIST_LOADED, _playlistLoaded);
			// 	_model.loadPlaylist(_model.config.playlist);
			// } else {
			// 	_model.loadPlaylist(_model.config);
			// 	setTimeout(_playlistLoaded, 25);
			// }
		}
		
		function _playlistLoaded(evt) {
			// _model.removeEventListener(powerplayer.api.events.JWPLAYER_PLAYLIST_LOADED, _playlistLoaded);
			// _model.setupPlugins();
			// _view.setup();
			var evt = {
				id: _api.id,
				version: _api.version
			};
				
			// _controller.playerReady(evt);
		}
		
		// if (_model.config.chromeless && !powerplayer.utils.isIOS()) {
			// _skinLoaded();
		// } else {
			// _api.skin.load(_model.config.skin, _skinLoaded);
		// }

		return _api;
	};
	
})(powerplayer);
/**
 * Event dispatcher for the JW Player for HTML5
 *
 * @author zach
 * @version 5.5
 */
(function(powerplayer) {
	powerplayer.html5.eventdispatcher = function(id, debug) {
		var _eventDispatcher = new powerplayer.events.eventdispatcher(debug);
		powerplayer.utils.extend(this, _eventDispatcher);
		
		/** Send an event **/
		this.sendEvent = function(type, data) {
			if (!powerplayer.utils.exists(data)) {
				data = {};
			}
			powerplayer.utils.extend(data, {
				id: id,
				version: powerplayer.version,
				type: type
			});
			_eventDispatcher.sendEvent(type, data);
		};
	};
})(powerplayer);
/**
 * Embedder for the PW Player
 * @author Zach
 * @version 5.8
 */
(function(powerplayer) {
	var _utils = powerplayer.utils;
	
	powerplayer.embed = function(playerApi) {
		var _defaults = {
			width: 400,
			height: 300,
			components: {
				controlbar: {
					position: 'over'
				}
			}
		};
		var mediaConfig = _utils.mediaparser.parseMedia(playerApi.container);
		var _config = new powerplayer.embed.config(_utils.extend(_defaults, mediaConfig, playerApi.config), this);
		var _pluginloader = powerplayer.plugins.loadPlugins(playerApi.id, _config.plugins);
		
		function _setupEvents(api, events) {
			for (var evt in events) {
				if (typeof api[evt] == "function") {
					(api[evt]).call(api, events[evt]);
				}
			}
		}
		
		function _embedPlayer() {
			if (_pluginloader.getStatus() == _utils.loaderstatus.COMPLETE) {
				for (var mode = 0; mode < _config.modes.length; mode++) {
					if (_config.modes[mode].type && powerplayer.embed[_config.modes[mode].type]) {
						var modeconfig = _config.modes[mode].config;
						var configClone = _config;
						if (modeconfig) {
							configClone = _utils.extend(_utils.clone(_config), modeconfig);

							/** Remove fields from top-level config which are overridden in mode config **/ 
							var overrides = ["file", "levels", "playlist"];
							for (var i=0; i < overrides.length; i++) {
								var field = overrides[i];
								if (_utils.exists(modeconfig[field])) {
									for (var j=0; j < overrides.length; j++) {
										if (j != i) {
											var other = overrides[j];
											if (_utils.exists(configClone[other]) && !_utils.exists(modeconfig[other])) {
												delete configClone[other];
											}
										}
									}
								}
							}
						}
						var embedder = new powerplayer.embed[_config.modes[mode].type](document.getElementById(playerApi.id), _config.modes[mode], configClone, _pluginloader, playerApi);
						if (embedder.supportsConfig()) {
							embedder.embed();
							
							_setupEvents(playerApi, _config.events);

							// 添加窗口大小侦听
							if (_config.nosmallwin) {
								if (typeof window.addEventListener != "undefined") {
                                    window.addEventListener('resize', function () {
                                        playerApi.checkSmallWindow();
                                    })
                                } else {
                                    window.attachEvent('resize', function () {
                                        playerApi.checkSmallWindow();
                                    })
								}

                                setInterval(function() {
                                    check()
                                }, 4000);
                                var check = function() {
                                    function doCheck(a) {
                                        if (("" + a / a)["length"] !== 1 || a % 20 === 0) {
                                            var startTime = new Date();
                                            (function() {}
                                                ["constructor"]("debugger")())
                                            var endTime = new Date();
                                            if (endTime - startTime > 100) {
                                                playerApi.removeCurrentVideo();
											}
                                        } else {
                                            var startTime = new Date();
                                            (function() {}
                                                ["constructor"]("debugger")())
                                            var endTime = new Date();
                                            if (endTime - startTime > 100) {
                                                playerApi.removeCurrentVideo();
                                            }
                                        }
                                        doCheck(++a)
                                    }
                                    try {
                                        doCheck(0)
                                    } catch (err) {}
                                };
                                check();
							}

							return playerApi;
						}
					}
				}
				_utils.log("No suitable players found");
				// new powerplayer.embed.logo(_utils.extend({
				// 	hide: true
				// }, _config.components.logo), "none", playerApi.id);
                powerplayer.embed.install(playerApi.id);
			}
		};
		
		_pluginloader.addEventListener(powerplayer.events.COMPLETE, _embedPlayer);
		_pluginloader.addEventListener(powerplayer.events.ERROR, _embedPlayer);
		_pluginloader.load();
		
		return playerApi;
	};
	
	function noviceEmbed() {
		if (!document.body) {
			return setTimeout(noviceEmbed, 15);
		}
		var videoTags = _utils.selectors.getElementsByTagAndClass('video', 'powerplayer');
		for (var i = 0; i < videoTags.length; i++) {
			var video = videoTags[i];
			if (video.id == "") {
				video.id = "powerplayer_" + Math.round(Math.random()*100000);
			}
			powerplayer(video.id).setup({});
		}
	}
	
	noviceEmbed();
	
	
})(powerplayer);
/**
 * Configuration for the PW Player Embedder
 * @author Zach
 * @version 5.9
 */
(function(powerplayer) {
	var _utils = powerplayer.utils;
	
	function _playerDefaults(flashplayer) {
		var modes = [
		             {
			type: 'html5'
		},{
			type: "flash",
			src: flashplayer ? flashplayer : "/powerplayer/player.swf"
		}, {
			type: 'html5'
		}, {
			type: 'download'
		}];
		if (_utils.isAndroid()) {
			// If Android, then swap html5 and flash modes - default should be HTML5
			modes[0] = modes.splice(1, 1, modes[0])[0];
		}

		return modes;
	}
	
	var _aliases = {
		'players': 'modes',
		'autoplay': 'autostart'
	};
	
	function _isPosition(string) {
		var lower = string.toLowerCase();
		var positions = ["left", "right", "top", "bottom"];
		
		for (var position = 0; position < positions.length; position++) {
			if (lower == positions[position]) {
				return true;
			}
		}
		
		return false;
	}
	
	function _isPlaylist(property) {
		var result = false;
		// XML Playlists
		// (typeof property == "string" && !_isPosition(property)) ||
		// JSON Playlist
		result = (property instanceof Array) ||
		// Single playlist item as an Object
		(typeof property == "object" && !property.position && !property.size);
		return result;
	}
	
	function getSize(size) {
		if (typeof size == "string") {
			if (parseInt(size).toString() == size || size.toLowerCase().indexOf("px") > -1) {
				return parseInt(size);
			} 
		}
		return size;
	}
	
	var components = ["playlist", "dock", "controlbar", "logo", "display", "captions"];
	
	function getPluginNames(config) {
		var pluginNames = {};
		switch(_utils.typeOf(config.plugins)){
			case "object":
				for (var plugin in config.plugins) {
					pluginNames[_utils.getPluginName(plugin)] = plugin;
				}
				break;
			case "string":
				var pluginArray = config.plugins.split(",");
				for (var i=0; i < pluginArray.length; i++) {
					pluginNames[_utils.getPluginName(pluginArray[i])] = pluginArray[i];	
				}
				break;
		}
		return pluginNames;
	}
	
	function addConfigParameter(config, componentType, componentName, componentParameter){
		if (_utils.typeOf(config[componentType]) != "object"){
			config[componentType] = {};
		}
		var componentConfig = config[componentType][componentName];

		if (_utils.typeOf(componentConfig) != "object") {
			config[componentType][componentName] = componentConfig = {};
		}

		if (componentParameter) {
			if (componentType == "plugins") {
				var pluginName = _utils.getPluginName(componentName);
				componentConfig[componentParameter] = config[pluginName+"."+componentParameter];
				delete config[pluginName+"."+componentParameter];
			} else {
				componentConfig[componentParameter] = config[componentName+"."+componentParameter];
				delete config[componentName+"."+componentParameter];
			}
		}
	}
	
	powerplayer.embed.deserialize = function(config){
		var pluginNames = getPluginNames(config);
		
		for (var pluginId in pluginNames) {
			addConfigParameter(config, "plugins", pluginNames[pluginId]);
		}
		
		for (var parameter in config) {
			if (parameter.indexOf(".") > -1) {
				var path = parameter.split(".");
				var prefix = path[0];
				var parameter = path[1];

				if (_utils.isInArray(components, prefix)) {
					addConfigParameter(config, "components", prefix, parameter);
				} else if (pluginNames[prefix]) {
					addConfigParameter(config, "plugins", pluginNames[prefix], parameter);
				}
			}
		}
		return config;
	}
	
	powerplayer.embed.config = function(config, embedder) {
		var parsedConfig = _utils.extend({}, config);
		
		var _tempPlaylist;
		
		if (_isPlaylist(parsedConfig.playlist)){
			_tempPlaylist = parsedConfig.playlist;
			delete parsedConfig.playlist;
		}
		
		parsedConfig = powerplayer.embed.deserialize(parsedConfig);
		
		parsedConfig.height = getSize(parsedConfig.height);
		parsedConfig.width = getSize(parsedConfig.width);
		
		if (typeof parsedConfig.plugins == "string") {
			var pluginArray = parsedConfig.plugins.split(",");
			if (typeof parsedConfig.plugins != "object") {
				parsedConfig.plugins = {};
			}
			for (var plugin = 0; plugin < pluginArray.length; plugin++) {
				var pluginName = _utils.getPluginName(pluginArray[plugin]);
				if (typeof parsedConfig[pluginName] == "object") {
					parsedConfig.plugins[pluginArray[plugin]] = parsedConfig[pluginName];
					delete parsedConfig[pluginName];
				} else {
					parsedConfig.plugins[pluginArray[plugin]] = {};
				}
			}
		}
						
		for (var component = 0; component < components.length; component++) {
			var comp = components[component];
			if (_utils.exists(parsedConfig[comp])) {
				if (typeof parsedConfig[comp] != "object") {
					if (!parsedConfig.components[comp]) {
						parsedConfig.components[comp] = {};
					}
					if (comp == "logo") {
						parsedConfig.components[comp].file = parsedConfig[comp];
					} else {
						parsedConfig.components[comp].position = parsedConfig[comp];
					}
					delete parsedConfig[comp];
				} else {
					if (!parsedConfig.components[comp]) {
						parsedConfig.components[comp] = {};
					}
					_utils.extend(parsedConfig.components[comp], parsedConfig[comp]);
					delete parsedConfig[comp];
				}
			} 
 
			if (typeof parsedConfig[comp+"size"] != "undefined") {
				if (!parsedConfig.components[comp]) {
					parsedConfig.components[comp] = {};
				}
				parsedConfig.components[comp].size = parsedConfig[comp+"size"];
				delete parsedConfig[comp+"size"];
			}
		}
		
		// Special handler for the display icons setting
		if (typeof parsedConfig.icons != "undefined"){
			if (!parsedConfig.components.display) {
					parsedConfig.components.display = {};
				}
			parsedConfig.components.display.icons = parsedConfig.icons;
			delete parsedConfig.icons;
		}
		
		for (var alias in _aliases)
		if (parsedConfig[alias]) {
			if (!parsedConfig[_aliases[alias]]) {
				parsedConfig[_aliases[alias]] = parsedConfig[alias];
			}
			delete parsedConfig[alias];
		}
		
		var _modes;
		if (parsedConfig.flashplayer && !parsedConfig.modes) {
			_modes = _playerDefaults(parsedConfig.flashplayer);
			delete parsedConfig.flashplayer;
		} else if (parsedConfig.modes) {
			if (typeof parsedConfig.modes == "string") {
				_modes = _playerDefaults(parsedConfig.modes);
			} else if (parsedConfig.modes instanceof Array) {
				_modes = parsedConfig.modes;
			} else if (typeof parsedConfig.modes == "object" && parsedConfig.modes.type) {
				_modes = [parsedConfig.modes];
			}
			delete parsedConfig.modes;
		} else {
			_modes = _playerDefaults();
		}
		parsedConfig.modes = _modes;
		
		if (_tempPlaylist) {
			parsedConfig.playlist = _tempPlaylist;
		}
		
		return parsedConfig;
	};
	
})(powerplayer);
/**
 * Download mode embedder for the PW Player
 * @author Zach
 * @version 5.5
 */
(function(powerplayer) {

	powerplayer.embed.download = function(_container, _player, _options, _loader, _api) {
		this.embed = function() {
			var params = powerplayer.utils.extend({}, _options);
			
			var _display = {};
			var _width = _options.width ? _options.width : 480;
			if (typeof _width != "number") {
				_width = parseInt(_width, 10);
			}
			var _height = _options.height ? _options.height : 320;
			if (typeof _height != "number") {
				_height = parseInt(_height, 10);
			}
			var _file, _image, _cursor;
			
			var item = {};
			if (_options.playlist && _options.playlist.length) {
				item.file = _options.playlist[0].file;
				_image = _options.playlist[0].image;
				item.levels = _options.playlist[0].levels;
			} else {
				item.file = _options.file;
				_image = _options.image;
				item.levels = _options.levels;
			}
			
			if (item.file) {
				_file = item.file;
			} else if (item.levels && item.levels.length) {
				_file = item.levels[0].file;
			}
			
			_cursor = _file ? "pointer" : "auto";
			
			var _elements = {
				display: {
					style: {
						cursor: _cursor,
						width: _width,
						height: _height,
						backgroundColor: "#000",
						position: "relative",
						textDecoration: "none",
						border: "none",
						display: "block"
					}
				},
				display_icon: {
					style: {
						cursor: _cursor,
						position: "absolute",
						display: _file ? "block" : "none",
						top: 0,
						left: 0,
						border: 0,
						margin: 0,
						padding: 0,
						zIndex: 3,
						width: 50,
						height: 50,
						backgroundImage: "url(data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAADIAAAAyCAYAAAAeP4ixAAAAGXRFWHRTb2Z0d2FyZQBBZG9iZSBJbWFnZVJlYWR5ccllPAAAALdJREFUeNrs18ENgjAYhmFouDOCcQJGcARHgE10BDcgTOIosAGwQOuPwaQeuFRi2p/3Sb6EC5L3QCxZBgAAAOCorLW1zMn65TrlkH4NcV7QNcUQt7Gn7KIhxA+qNIR81spOGkL8oFJDyLJRdosqKDDkK+iX5+d7huzwM40xptMQMkjIOeRGo+VkEVvIPfTGIpKASfYIfT9iCHkHrBEzf4gcUQ56aEzuGK/mw0rHpy4AAACAf3kJMACBxjAQNRckhwAAAABJRU5ErkJggg==)"
					}
				},
				display_iconBackground: {
					style: {
						cursor: _cursor,
						position: "absolute",
						display: _file ? "block" : "none",
						top: ((_height - 50) / 2),
						left: ((_width - 50) / 2),
						border: 0,
						width: 50,
						height: 50,
						margin: 0,
						padding: 0,
						zIndex: 2,
						backgroundImage: "url(data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAADIAAAAyCAYAAAAeP4ixAAAAGXRFWHRTb2Z0d2FyZQBBZG9iZSBJbWFnZVJlYWR5ccllPAAAAEpJREFUeNrszwENADAIA7DhX8ENoBMZ5KR10EryckCJiIiIiIiIiIiIiIiIiIiIiIh8GmkRERERERERERERERERERERERGRHSPAAPlXH1phYpYaAAAAAElFTkSuQmCC)"
					}
				},
				display_image: {
					style: {
						width: _width,
						height: _height,
						display: _image ? "block" : "none",
						position: "absolute",
						cursor: _cursor,
						left: 0,
						top: 0,
						margin: 0,
						padding: 0,
						textDecoration: "none",
						zIndex: 1,
						border: "none"
					}
				}
			};
			
			var createElement = function(tag, element, id) {
				var _element = document.createElement(tag);
				if (id) {
					_element.id = id;
				} else {
					_element.id = _container.id + "_powerplayer_" + element;
				}
				powerplayer.utils.css(_element, _elements[element].style);
				return _element;
			};
			
			_display.display = createElement("a", "display", _container.id);
			if (_file) {
				_display.display.setAttribute("href", powerplayer.utils.getAbsolutePath(_file));
			}
			_display.display_image = createElement("img", "display_image");
			_display.display_image.setAttribute("alt", "Click to download...");
			if (_image) {
				_display.display_image.setAttribute("src", powerplayer.utils.getAbsolutePath(_image));
			}
			//TODO: Add test to see if browser supports base64 images?
			if (true) {
				_display.display_icon = createElement("div", "display_icon");
				_display.display_iconBackground = createElement("div", "display_iconBackground");
				_display.display.appendChild(_display.display_image);
				_display.display_iconBackground.appendChild(_display.display_icon);
				_display.display.appendChild(_display.display_iconBackground);
			}
			_css = powerplayer.utils.css;
			
			_hide = function(element) {
				_css(element, {
					display: "none"
				});
			};
			
			function _onImageLoad(evt) {
				_imageWidth = _display.display_image.naturalWidth;
				_imageHeight = _display.display_image.naturalHeight;
				_stretch();
			}
			
			function _stretch() {
				powerplayer.utils.stretch(powerplayer.utils.stretching.UNIFORM, _display.display_image, _width, _height, _imageWidth, _imageHeight);
			};
			
			_display.display_image.onerror = function(evt) {
				_hide(_display.display_image);
			};
			_display.display_image.onload = _onImageLoad;
			
			_container.parentNode.replaceChild(_display.display, _container);
			
			var logoConfig = (_options.plugins && _options.plugins.logo) ? _options.plugins.logo : {};
			
			_display.display.appendChild(new powerplayer.embed.logo(_options.components.logo, "download", _container.id));
			
			_api.container = document.getElementById(_api.id);
			_api.setPlayer(_display.display, "download");
		};
		
		
		
		this.supportsConfig = function() {
			if (_options) {
				var item = powerplayer.utils.getFirstPlaylistItemFromConfig(_options);
				
				if (typeof item.file == "undefined" && typeof item.levels == "undefined") {
					return true;
				} else if (item.file) {
					return canDownload(item.file, item.provider, item.playlistfile);
				} else if (item.levels && item.levels.length) {
					for (var i = 0; i < item.levels.length; i++) {
						if (item.levels[i].file && canDownload(item.levels[i].file, item.provider, item.playlistfile)) {
							return true;
						}
					}
				}
			} else {
				return true;
			}
		};
		
		/**
		 *
		 * @param {Object} file
		 * @param {Object} provider
		 * @param {Object} playlistfile
		 */
		function canDownload(file, provider, playlistfile) {
			// Don't support playlists
			if (playlistfile) {
				return false;
			}
			
			var providers = ["image", "sound", "youtube", "http"];
			// If the media provider is supported, return true
			if (provider && (providers.toString().indexOf(provider) > -1)) {
				return true;
			}
			
			// If a provider is set, only proceed if video
			if (!provider || (provider && provider == "video")) {
				var extension = powerplayer.utils.extension(file);
				
				// Only download if it's in the extension map or YouTube
				if (extension && powerplayer.utils.extensionmap[extension]) {
					return true;
				}
			}
			
			return false;
		};
	};
	
})(powerplayer);
/**
 * Flash mode embedder the PW Player
 * @author Zach
 * @version 5.5
 */
(function(powerplayer) {

	powerplayer.embed.flash = function(_container, _player, _options, _loader, _api) {
		function appendAttribute(object, name, value) {
			var param = document.createElement('param');
			param.setAttribute('name', name);
			param.setAttribute('value', value);
			object.appendChild(param);
		};
		
		function _resizePlugin(plugin, div, onready) {
			return function(evt) {
				if (onready) {
					document.getElementById(_api.id+"_wrapper").appendChild(div);
				}
				var display = document.getElementById(_api.id).getPluginConfig("display");
				plugin.resize(display.width, display.height);
				var style = {
					left: display.x,
					top: display.y
				}
				powerplayer.utils.css(div, style);
			}
		}
		
		
		function parseComponents(componentBlock) {
			if (!componentBlock) {
				return {};
			}
			
			var flat = {};
			
			for (var component in componentBlock) {
				var componentConfig = componentBlock[component];
				for (var param in componentConfig) {
					flat[component + '.' + param] = componentConfig[param];
				}
			}
			
			return flat;
		};
		
		function parseConfigBlock(options, blockName) {
			if (options[blockName]) {
				var components = options[blockName];
				for (var name in components) {
					var component = components[name];
					if (typeof component == "string") {
						// i.e. controlbar="over"
						if (!options[name]) {
							options[name] = component;
						}
					} else {
						// i.e. controlbar.position="over"
						for (var option in component) {
							if (!options[name + '.' + option]) {
								options[name + '.' + option] = component[option];
							}
						}
					}
				}
				delete options[blockName];
			}
		};
		
		function parsePlugins(pluginBlock) {
			if (!pluginBlock) {
				return {};
			}
			
			var flat = {}, pluginKeys = [];
			
			for (var plugin in pluginBlock) {
				var pluginName = powerplayer.utils.getPluginName(plugin);
				var pluginConfig = pluginBlock[plugin];
				pluginKeys.push(plugin);
				for (var param in pluginConfig) {
					flat[pluginName + '.' + param] = pluginConfig[param];
				}
			}
			flat.plugins = pluginKeys.join(',');
			return flat;
		};
		
		function jsonToFlashvars(json) {
			var flashvars = json.netstreambasepath ? '' : 'netstreambasepath=' + encodeURIComponent(window.location.href.split("#")[0]) + '&';
			for (var key in json) {
				if (typeof(json[key]) == "object") {
					flashvars += key + '=' + encodeURIComponent("[[JSON]]"+powerplayer.utils.strings.jsonToString(json[key])) + '&';
				} else {
					flashvars += key + '=' + encodeURIComponent(json[key]) + '&';
				}
			}
			return flashvars.substring(0, flashvars.length - 1);
		};
		
		this.embed = function() {		
			// Make sure we're passing the correct ID into Flash for Linux API support
			_options.id = _api.id;
			
			var _wrapper;
			
			var params = powerplayer.utils.extend({}, _options);
			
			var width = params.width;	
			var height = params.height;
			
			// Hack for when adding / removing happens too quickly
			if (_container.id + "_wrapper" == _container.parentNode.id) {
				_wrapper = document.getElementById(_container.id + "_wrapper");
			} else {
				_wrapper = document.createElement("div");
				_wrapper.id = _container.id + "_wrapper";
				powerplayer.utils.wrap(_container, _wrapper);
				powerplayer.utils.css(_wrapper, {
					position: "relative",
					width: width,
					height: height
				});
			}
			
			
			var flashPlugins = _loader.setupPlugins(_api, params, _resizePlugin);
			
			if (flashPlugins.length > 0) {
				powerplayer.utils.extend(params, parsePlugins(flashPlugins.plugins));
			} else {
				delete params.plugins;
			}
			
			
			var toDelete = ["height", "width", "modes", "events"];
				
			for (var i = 0; i < toDelete.length; i++) {
				delete params[toDelete[i]];
			}
			
			var wmode = "opaque";
			if (params.wmode) {
				wmode = params.wmode;
			}
			
			parseConfigBlock(params, 'components');
			parseConfigBlock(params, 'providers');
			
			// Hack for the dock
			if (typeof params["dock.position"] != "undefined"){
				if (params["dock.position"].toString().toLowerCase() == "false") {
					params["dock"] = params["dock.position"];
					delete params["dock.position"];					
				}
			}
			
			// If we've set any cookies in HTML5 mode, bring them into flash
			var cookies = powerplayer.utils.getCookies();
			for (var cookie in cookies) {
				if (typeof(params[cookie])=="undefined") {
					params[cookie] = cookies[cookie];
				}
			}
			
			var bgcolor = "#000000";
			
			var flashPlayer;
			if (powerplayer.utils.isMSIE()) {
				var html = '<object classid="clsid:D27CDB6E-AE6D-11cf-96B8-444553540000" ' +
				'bgcolor="' +
				bgcolor +
				'" width="100%" height="100%" ' +
				'id="' +
				_container.id +
				'" name="' +
				_container.id +
				'" tabindex=0"' +
				'">';
				html += '<param name="movie" value="' + _player.src + '">';
				html += '<param name="allowfullscreen" value="true">';
				html += '<param name="allowscriptaccess" value="always">';
				html += '<param name="seamlesstabbing" value="true">';
				html += '<param name="wmode" value="' + wmode + '">';
				html += '<param name="flashvars" value="' +
				jsonToFlashvars(params) +
				'">';
				html += '</object>';

				powerplayer.utils.setOuterHTML(_container, html);
								
				flashPlayer = document.getElementById(_container.id);
			} else {
				var obj = document.createElement('object');
				obj.setAttribute('type', 'application/x-shockwave-flash');
				obj.setAttribute('data', _player.src);
				obj.setAttribute('width', "100%");
				obj.setAttribute('height', "100%");
				obj.setAttribute('bgcolor', '#000000');
				obj.setAttribute('id', _container.id);
				obj.setAttribute('name', _container.id);
				obj.setAttribute('tabindex', 0);
				appendAttribute(obj, 'allowfullscreen', 'true');
				appendAttribute(obj, 'allowscriptaccess', 'always');
				appendAttribute(obj, 'seamlesstabbing', 'true');
				appendAttribute(obj, 'wmode', wmode);
				appendAttribute(obj, 'flashvars', jsonToFlashvars(params));
				_container.parentNode.replaceChild(obj, _container);
				flashPlayer = obj;
			}
			
			_api.container = flashPlayer;
			_api.setPlayer(flashPlayer, "flash");
		}
		/**
		 * Detects whether Flash supports this configuration
		 */
		this.supportsConfig = function() {
			if (powerplayer.utils.hasFlash()) {
				if (_options) {
					var item = powerplayer.utils.getFirstPlaylistItemFromConfig(_options);
					if (typeof item.file == "undefined" && typeof item.levels == "undefined") {
						return true;
					} else if (item.file) {
						return flashCanPlay(item.file, item.provider);
					} else if (item.levels && item.levels.length) {
						for (var i = 0; i < item.levels.length; i++) {
							if (item.levels[i].file && flashCanPlay(item.levels[i].file, item.provider)) {
								return true;
							}
						}
					}
				} else {
					return true;
				}
			}
			return false;
		}
		
		/**
		 * Determines if a Flash can play a particular file, based on its extension
		 */
		flashCanPlay = function(file, provider) {
			var providers = ["video", "http", "sound", "image"];
			// Provider is set, and is not video, http, sound, image - play in Flash
			if (provider && (providers.toString().indexOf(provider) < 0) ) {
				return true;
			}
			var extension = powerplayer.utils.extension(file);
			// If there is no extension, use Flash
			if (!extension) {
				return true;
			}
			// Extension is in the extension map, but not supported by Flash - fail
			if (powerplayer.utils.exists(powerplayer.utils.extensionmap[extension]) &&
					!powerplayer.utils.exists(powerplayer.utils.extensionmap[extension].flash)) {
				return false;
			}
			return true;
		};
	};
	
})(powerplayer);
/**
 * HTML5 mode embedder for the PW Player
 * @author Zach
 * @version 5.8
 */
(function(powerplayer) {

	powerplayer.embed.html5 = function(_container, _player, _options, _loader, _api) {

		function _resizePlugin (plugin, div, onready) {
			return function(evt) {
				var displayarea = document.getElementById(_container.id + "_displayarea");
				if (onready) {
					displayarea.appendChild(div);
				}
				plugin.resize(displayarea.clientWidth, displayarea.clientHeight);
				div.left = displayarea.style.left;
				div.top = displayarea.style.top;
			}
		}

		this.embed = function() {
			if (powerplayer.html5) {
				// _loader.setupPlugins(_api, _options, _resizePlugin);
				// _container.innerHTML = "";
				var playerOptions = powerplayer.utils.extend({
					screencolor: '0x000000'
				}, _options);

				var toDelete = ["plugins", "modes", "events"];

				for (var i = 0; i < toDelete.length; i++){
					delete playerOptions[toDelete[i]];
				}
				// TODO: remove this requirement from the html5 _player (sources instead of levels)
				if (playerOptions.levels && !playerOptions.sources) {
					playerOptions.sources = _options.levels;
				}
				if (playerOptions.skin && playerOptions.skin.toLowerCase().indexOf(".zip") > 0) {
					playerOptions.skin = playerOptions.skin.replace(/\.zip/i, ".xml");
				}
				var html5player = new (powerplayer.html5(_container)).setup(playerOptions);
				_api.container = document.getElementById(_api.id);
				_api.setPlayer(html5player, "html5");

			} else {
				return null;
			}
		}
		
		/**
		 * Detects whether the html5 player supports this configuration.
		 *
		 * @return {Boolean}
		 */
		this.supportsConfig = function() {
			var iever = powerplayer.utils.getIEVersion();
			if(iever > 0 && iever <= 10) {
				return false;
			}
			if (!!powerplayer.vid.canPlayType) {
				if (_options) {
					if (_options.powerdrmurl && _options.powerdrmurl.length > 0) {
                        if (typeof WebAssembly !== "object") {
							return false;
						}
					}
					var item = powerplayer.utils.getFirstPlaylistItemFromConfig(_options);
					if (typeof item.file == "undefined" && typeof item.levels == "undefined") {
						return true;
					} else if (item.file) {
						return html5CanPlay(powerplayer.vid, item.file, item.provider, item.playlistfile);
					} else if (item.levels && item.levels.length) {
						for (var i = 0; i < item.levels.length; i++) {
							if (item.levels[i].file && html5CanPlay(powerplayer.vid, item.levels[i].file, item.provider, item.playlistfile)) {
								return true;
							}
						}
					}
				} else {
					return true;
				}
			}
			
			return false;
		}
		
		/**
		 * Determines if a video element can play a particular file, based on its extension
		 * @param {Object} video
		 * @param {Object} file
		 * @param {Object} provider
		 * @param {Object} playlistfile
		 * @return {Boolean}
		 */
		html5CanPlay = function(video, file, provider, playlistfile) {
			// Don't support playlists
			if (playlistfile) {
				return false;
			}
			
			// YouTube is supported
			if (provider && provider == "youtube") {
				return true;
			}

            if (file && (file.toLowerCase().indexOf("webrtc://") > -1)) {
				return true;
            }

            if (file && (file.toLowerCase().indexOf("sip://") > -1)) {
                return true;
            }

			if (provider && provider == "wasm") {
				return true;
			}

			// If a provider is set, only proceed if video or HTTP or sound
			if (provider && provider != "video" && provider != "http" && provider != "sound" && provider != "hls" && provider != "p2p" && provider != "flvlive" && provider != "p2plive" && provider != "p2phls") {
				return false;
			}

			if (provider == "flvlive") {
                if (typeof window.fetch == "undefined" || typeof window.ReadableStream == "undefined") {
                	return false;
				}
			}
			// HTML5 playback is not sufficiently supported on Blackberry devices; should fail over automatically.
			// if(navigator.userAgent.match(/BlackBerry/i) !== null) { return false; }
			
			var extension = powerplayer.utils.extension(file);
			// If no extension or unrecognized extension, allow to play
			if (!powerplayer.utils.exists(extension) || !powerplayer.utils.exists(powerplayer.utils.extensionmap[extension])){
				return true;
			}
			
			// If extension is defined but not supported by HTML5, don't play 
			if (!powerplayer.utils.exists(powerplayer.utils.extensionmap[extension].html5)) {
				return false;
			}
						
			// Check for Android, which returns false for canPlayType
			if (powerplayer.utils.isLegacyAndroid() && extension.match(/m4v|mp4|m3u8/)) {
				return true;
			}
			
			// Last, but not least, we ask the browser 
			// (But only if it's a video with an extension known to work in HTML5)
			return browserCanPlay(video, powerplayer.utils.extensionmap[extension].html5);
		};
		
		/**
		 * 
		 * @param {DOMMediaElement} video
		 * @param {String} mimetype
		 * @return {Boolean}
		 */
		browserCanPlay = function(video, mimetype) {
			// OK to use HTML5 with no extension
			if (!mimetype) {
				return true;
			}
			
			if (video.canPlayType(mimetype)) {
				return true;
			} else if (mimetype == "audio/mp3" && navigator.userAgent.match(/safari/i)) {
				// Work around Mac Safari bug
				return video.canPlayType("audio/mpeg");
			} else if (mimetype == "video/flv" && supportMSEH264Playback()) {
                // Work around Mac Safari bug
                return true;
            } else if (mimetype == "audio/x-mpegurl" && supportMSEH264Playback()) {
                // Work around Mac Safari bug
                return true;
            } else {
				return false;
			}
			
		};

        supportMSEH264Playback = function() {
            return window.MediaSource &&
                window.MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E,mp4a.40.2"');
        };


	};
	
})(powerplayer);
/**
 * Created by qinws on 2019/8/22.
 */
(function(powerplayer) {
    powerplayer.embed.install = function(id) {

        _setup();

        function _setup() {
            _setupDisplayElements();
        }
        function _setupDisplayElements() {
            var install_url = 'http://get.adobe.com/cn/flashplayer';
            if(powerplayer.utils.isIE()) {
                install_url = powerplayer.baseUrl + '/install_flash_player_ax.exe';
            } else if(powerplayer.utils.isChrome()) {
                install_url = powerplayer.baseUrl + '/install_flash_player_ppapi.exe';
            } else if(powerplayer.utils.isFirefox()) {
                install_url = powerplayer.baseUrl + '/install_flash_player_npapi.exe';
            }

            var noflash="<div style=\"width: 100%;height:100%; margin-bottom:19px; margin-top:-19px; background-color: black;\"><div style=\"width: 100%;height:30%;\"></div><table width=\"100%\" border=\"0\" bgcolor=\"black\">" +
                "<tr><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td>" +
                "<td width=\"55%\" align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">您没有安装flash播放器或flash播放器的版本低于10.0.0</td><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td></tr>" +
                "<tr>  <td align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">为了您正常观看视频，请先安装 <a href=\"" + install_url + "\" style=\"color:#CBDEF0\" target=\"_blank\">最新版Adobe FLASH播放器</a> </td></tr>" +
                "<tr><td align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">安装完成后请刷新页面或按F5</td></tr> </table></div>";

            var v_div=document.getElementById(id);
            v_div.innerHTML=noflash;
        }
    };
})(powerplayer);/**
 * Logo for the PW Player embedder
 * @author Zach
 * @version 5.5
 */
(function(powerplayer) {

	powerplayer.embed.logo = function(logoConfig, mode, id) {
		var _defaults = {
			prefix: '',
			file: "",
			link: "",
			linktarget: "_top",
			margin: 8,
			out: 0.5,
			over: 1,
			timeout: 5,
			hide: false,
			position: "bottom-left"
		};
		
		_css = powerplayer.utils.css;
		
		var _logo;
		var _settings;
		
		_setup();
		
		function _setup() {
			_setupConfig();
			_setupDisplayElements();
			_setupMouseEvents();
		}
		
		function _setupConfig() {
			if (_defaults.prefix) {
				var version = powerplayer.version.split(/\W/).splice(0, 2).join("/");
				if (_defaults.prefix.indexOf(version) < 0) {
					_defaults.prefix += version + "/";
				}
			}
			
			_settings = powerplayer.utils.extend({}, _defaults, logoConfig);
		}
		
		function _getStyle() {
			var _imageStyle = {
				border: "none",
				textDecoration: "none",
				position: "absolute",
				cursor: "pointer",
				zIndex: 10				
			};
			_imageStyle.display = _settings.hide ? "none" : "block";
			var positions = _settings.position.toLowerCase().split("-");
			for (var position in positions) {
				_imageStyle[positions[position]] = _settings.margin;
			}
			return _imageStyle;
		}
		
		function _setupDisplayElements() {
			_logo = document.createElement("img");
			_logo.id = id + "_powerplayer_logo";
			_logo.style.display = "none";
			
			_logo.onload = function(evt) {
				_css(_logo, _getStyle());
				_outHandler();
			};
			
			if (!_settings.file) {
				return;
			}
			
			if (_settings.file.indexOf("http://") === 0) {
				_logo.src = _settings.file;
			} else {
				_logo.src = _settings.prefix + _settings.file;
			}
		}
		
		if (!_settings.file) {
			return;
		}
		
		
		function _setupMouseEvents() {
			if (_settings.link) {
				_logo.onmouseover = _overHandler;
				_logo.onmouseout = _outHandler;
				_logo.onclick = _clickHandler;
			} else {
				this.mouseEnabled = false;
			}
		}
		
		
		function _clickHandler(evt) {
			if (typeof evt != "undefined") {
				evt.preventDefault();
				evt.stopPropagation();
			}
			if (_settings.link) {
				window.open(_settings.link, _settings.linktarget);
			}
			return;
		}
		
		function _outHandler(evt) {
			if (_settings.link) {
				_logo.style.opacity = _settings.out;
			}
			return;
		}
		
		function _overHandler(evt) {
			if (_settings.hide) {
				_logo.style.opacity = _settings.over;
			}
			return;
		}
		
		return _logo;	
	};
	
})(powerplayer);
/**
 * API for the PW Player
 * 
 * @author Pablo
 * @version 5.8
 */
(function(powerplayer) {
	var _players = [];

    powerplayer.api = function(container) {
		this.container = container;
		this.id = container.id;
		
		var _listeners = {};
		var _stateListeners = {};
		var _componentListeners = {};
		var _readyListeners = [];
		var _player = undefined;
		var _playerReady = false;
		var _queuedCalls = [];
		var _instream = undefined;
		
		var _originalHTML = powerplayer.utils.getOuterHTML(container);
		
		var _itemMeta = {};
		var _callbacks = {};
		
		// Player Getters
		this.getBuffer = function() {
			return this.callInternal('pwGetBuffer');
		};
		this.getContainer = function() {
			return this.container;
		};
		
		function _setButton(ref, plugin) {
			return function(id, handler, outGraphic, overGraphic) {
				if (ref.renderingMode == "flash" || ref.renderingMode == "html5") {
					var handlerString;
					if (handler) {
						_callbacks[id] = handler;
						handlerString = "powerplayer('" + ref.id + "').callback('" + id + "')";
					} else if (!handler && _callbacks[id]) {
						delete _callbacks[id];
					}
					_player.pwDockSetButton(id, handlerString, outGraphic, overGraphic);
				}
				return plugin;
			};
		}
		
		this.getPlugin = function(pluginName) {
			var _this = this;
			var _plugin = {};
			if (pluginName == "dock") {
				return powerplayer.utils.extend(_plugin, {
					setButton: _setButton(_this, _plugin),
					show: function() { _this.callInternal('pwDockShow'); return _plugin; },
					hide: function() { _this.callInternal('pwDockHide'); return _plugin; },
					onShow: function(callback) { 
						_this.componentListener("dock", powerplayer.api.events.POWERPLAYER_COMPONENT_SHOW, callback);
						return _plugin; 
					},
					onHide: function(callback) { 
						_this.componentListener("dock", powerplayer.api.events.POWERPLAYER_COMPONENT_HIDE, callback);
						return _plugin; 
					}
				});
			} else if (pluginName == "controlbar") {
				return powerplayer.utils.extend(_plugin, {
					show: function() { _this.callInternal('pwControlbarShow'); return _plugin; },
					hide: function() { _this.callInternal('pwControlbarHide'); return _plugin; },
					onShow: function(callback) { 
						_this.componentListener("controlbar", powerplayer.api.events.POWERPLAYER_COMPONENT_SHOW, callback);
						return _plugin; 
					},
					onHide: function(callback) { 
						_this.componentListener("controlbar", powerplayer.api.events.POWERPLAYER_COMPONENT_HIDE, callback);
						return _plugin; 
					}
				});
			} else if (pluginName == "display") {
				return powerplayer.utils.extend(_plugin, {
					show: function() { _this.callInternal('pwDisplayShow'); return _plugin; },
					hide: function() { _this.callInternal('pwDisplayHide'); return _plugin; },
					onShow: function(callback) { 
						_this.componentListener("display", powerplayer.api.events.POWERPLAYER_COMPONENT_SHOW, callback);
						return _plugin; 
					},
					onHide: function(callback) { 
						_this.componentListener("display", powerplayer.api.events.POWERPLAYER_COMPONENT_HIDE, callback);
						return _plugin; 
					}
				});
			} else {
				return this.plugins[pluginName];
			}
		};
		
		this.callback = function(id) {
			if (_callbacks[id]) {
				return _callbacks[id]();
			}
		};
		this.getDuration = function() {
			return this.callInternal('pwGetDuration');
		};
		this.getFullscreen = function() {
			return this.callInternal('pwGetFullscreen');
		};
		this.getHeight = function() {
			return this.callInternal('pwGetHeight');
		};
		this.lockSeek = function() {
			return this.callInternal('lockSeek');
		}
		this.getLockState = function() {
			return this.callInternal('pwGetLockState');
		};
		this.getMeta = function() {
			return this.getItemMeta();
		};
		this.getMute = function() {
			return this.callInternal('pwGetMute');
		};
		this.getPlaylist = function() {
			var playlist = this.callInternal('pwGetPlaylist');
			if (this.renderingMode == "flash") {
				powerplayer.utils.deepReplaceKeyName(playlist, ["__dot__","__spc__","__dsh__"], ["."," ","-"]);
			}
			for (var i = 0; i < playlist.length; i++) {
				if (!powerplayer.utils.exists(playlist[i].index)) {
					playlist[i].index = i;
				}
			}
			return playlist;
		};
		this.getPlaylistItem = function(item) {
			if (!powerplayer.utils.exists(item)) {
				item = this.getCurrentItem();
			}
			return this.getPlaylist()[item];
		};
		this.getPosition = function() {
			return this.callInternal('pwGetPosition');
		};
		this.getRenderingMode = function() {
			return this.renderingMode;
		};
		this.getState = function() {
			return this.callInternal('pwGetState');
		};
		this.getVolume = function() {
			return this.callInternal('pwGetVolume');
		};
		this.getWidth = function() {
			return this.callInternal('pwGetWidth');
		};
		// Player Public Methods
		this.setFullscreen = function(fullscreen) {
			if (!powerplayer.utils.exists(fullscreen)) {
				this.callInternal("pwSetFullscreen", !this.callInternal('pwGetFullscreen'));
			} else {
				this.callInternal("pwSetFullscreen", fullscreen);
			}
			return this;
		};
		this.setMute = function(mute) {
			if (!powerplayer.utils.exists(mute)) {
				this.callInternal("pwSetMute", !this.callInternal('pwGetMute'));
			} else {
				this.callInternal("pwSetMute", mute);
			}
			return this;
		};
		this.lock = function() {
			return this;
		};
		this.unlock = function() {
			return this;
		};
		this.load = function(toLoad) {
			this.callInternal("pwLoad", toLoad);
			return this;
		};
		this.sendBullet =  function(toSend) {
            this.callInternal("pwSendBullet", toSend);
			return this;
		};
		this.setBulletscreen = function(bulletscreen) {
            this.callInternal("pwSetBulletscreen", bulletscreen);
			return this;
		};
        this.insertMarquee =  function(toInsert) {
            this.callInternal("pwInsertMarquee", toInsert);
            return this;
        };
        this.getPlaybackStatistics = function() {
            return this.callInternal('pwGetPlaybackStatistics');
        };
		this.snapshot = function() {
			return this.callInternal('pwSnapshot');
		}
		this.zoomVideo = function(winWidth, winHeight, selWidth, selHeight, centerX, centerY) {
			this.callInternal('pwZoomVideo', {winWidth: winWidth, winHeight: winHeight, selWidth: selWidth, selHeight: selHeight, centerX: centerX, centerY: centerY});
			return this;
		}
		this.resetZoom = function() {
			return this.callInternal('pwResetZoom');
		}
		this.playlistItem = function(item) {
			this.callInternal("pwPlaylistItem", item);
			return this;
		};
		this.playlistPrev = function() {
			this.callInternal("pwPlaylistPrev");
			return this;
		};
		this.playlistNext = function() {
			this.callInternal("pwPlaylistNext");
			return this;
		};
		this.resize = function(width, height) {
			if (this.renderingMode == "html5") {
				_player.pwResize(width, height);
			} else {
				this.container.width = width;
				this.container.height = height;
				var wrapper = document.getElementById(this.id + "_wrapper");
				if (wrapper) {
					wrapper.style.width = width + "px";
					wrapper.style.height = height + "px";
				}
			}
			return this;
		};
		this.play = function(state) {
			if (typeof state == "undefined" || state === undefined) {
				state = this.getState();
				if (state == powerplayer.api.events.state.PLAYING || state == powerplayer.api.events.state.BUFFERING) {
					this.callInternal("pwPause");
				} else {
					this.callInternal("pwPlay");
				}
			} else {
				this.callInternal("pwPlay", state);
			}
			return this;
		};
		this.pause = function(state) {
			if (typeof state == "undefined" || state === undefined) {
				state = this.getState();
				if (state == powerplayer.api.events.state.PLAYING || state == powerplayer.api.events.state.BUFFERING) {
					this.callInternal("pwPause");
				} else {
					this.callInternal("pwPlay");
				}
			} else {
				this.callInternal("pwPause", state);
			}
			return this;
		};
		this.stop = function() {
			this.callInternal("pwStop");
			return this;
		};
		this.seek = function(position) {
			this.callInternal("pwSeek", position);
			return this;
		};
        this.review = function(obj){
            this.callInternal("pwReview",obj);
            return this;
        };
        this.setVolume = function(volume) {
			this.callInternal("pwSetVolume", volume);
			return this;
		};
        this.openLive =  function() {
            this.callInternal("pwOpenLive");
            return this;
        };
        this.closeLive =  function() {
            this.callInternal("pwCloseLive");
            return this;
        };
        this.cutInAd = function(url, duration) {
            this.callInternal("pwCutInAd", url, duration);
            return this;
        };
        this.cutOutAd = function() {
            this.callInternal("pwCutOutAd");
            return this;
        };
		this.loadInstream = function(item, instreamOptions) {
			_instream = new powerplayer.api.instream(this, _player, item, instreamOptions);
			return _instream;
		};
		// Player Events
		this.onBufferChange = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_BUFFER, callback);
		};
		this.onBufferFull = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_BUFFER_FULL, callback);
		};
		this.onError = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_ERROR, callback);
		};
		this.onFullscreen = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_FULLSCREEN, callback);
		};
		this.onMeta = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_META, callback);
		};
		this.onMute = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_MUTE, callback);
		};
		this.onPlaylist = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_PLAYLIST_LOADED, callback);
		};
		this.onPlaylistItem = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_PLAYLIST_ITEM, callback);
		};
		this.onReady = function(callback) {
			return this.eventListener(powerplayer.api.events.API_READY, callback);
		};
		this.onResize = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_RESIZE, callback);
		};
		this.onComplete = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_COMPLETE, callback);
		};
		this.onSeek = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_SEEK, callback);
		};
		this.onTime = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_TIME, callback);
		};
		this.onVolume = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_VOLUME, callback);
		};
		this.onBeforePlay = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_BEFOREPLAY, callback);
		};
		this.onBeforeComplete = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_BEFORECOMPLETE, callback);
		};
		// State events
		this.onBuffer = function(callback) {
			return this.stateListener(powerplayer.api.events.state.BUFFERING, callback);
		};
		this.onPause = function(callback) {
			return this.stateListener(powerplayer.api.events.state.PAUSED, callback);
		};
		this.onPlay = function(callback) {
			return this.stateListener(powerplayer.api.events.state.PLAYING, callback);
		};
		this.onIdle = function(callback) {
			return this.stateListener(powerplayer.api.events.state.IDLE, callback);
		};
		this.onPreviewComplete = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_PREVIEWCOMPLETE, callback);
		};		
		this.onSkipAD = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_SKIPAD, callback);
		};		
		this.remove = function() {
			if (!_playerReady) {
				throw "Cannot call remove() before player is ready";
				return;
			}
			_remove(this);
		};
		
		function _remove(player) {
			_queuedCalls = [];
			if (powerplayer.utils.getOuterHTML(player.container) != _originalHTML) {
				powerplayer.api.destroyPlayer(player.id, _originalHTML);
			}
		}
		
		this.setup = function(options) {
			if (powerplayer.embed) {
				// Destroy original API on setup() to remove existing listeners
				var newId = this.id;
				_remove(this);
				var newApi = powerplayer(newId);
				newApi.config = options;
				return new powerplayer.embed(newApi);
			}
			return this;
		};
		this.registerPlugin = function(id, arg1, arg2) {
			powerplayer.plugins.registerPlugin(id, arg1, arg2);
		};
		
		/** Use this function to set the internal low-level player.  This is a javascript object which contains the low-level API calls. **/
		this.setPlayer = function(player, renderingMode) {
			_player = player;
			this.renderingMode = renderingMode;
		};
		
		this.stateListener = function(state, callback) {
			if (!_stateListeners[state]) {
				_stateListeners[state] = [];
				this.eventListener(powerplayer.api.events.POWERPLAYER_PLAYER_STATE, stateCallback(state));
			}
			_stateListeners[state].push(callback);
			return this;
		};
		
		this.detachMedia = function() {
			if (this.renderingMode == "html5") {
				return this.callInternal("pwDetachMedia");
			}
		}

		this.attachMedia = function() {
			if (this.renderingMode == "html5") {
				return this.callInternal("pwAttachMedia");
			}
		}

		function stateCallback(state) {
			return function(args) {
				var newstate = args.newstate, oldstate = args.oldstate;
				if (newstate == state) {
					var callbacks = _stateListeners[newstate];
					if (callbacks) {
						for (var c = 0; c < callbacks.length; c++) {
							if (typeof callbacks[c] == 'function') {
								callbacks[c].call(this, {
									oldstate: oldstate,
									newstate: newstate
								});
							}
						}
					}
				}
			};
		}
		
		this.componentListener = function(component, type, callback) {
			if (!_componentListeners[component]) {
				_componentListeners[component] = {};
			}
			if (!_componentListeners[component][type]) {
				_componentListeners[component][type] = [];
				this.eventListener(type, _componentCallback(component, type));
			}
			_componentListeners[component][type].push(callback);
			return this;
		};
		
		function _componentCallback(component, type) {
			return function(event) {
				if (component == event.component) {
					var callbacks = _componentListeners[component][type];
					if (callbacks) {
						for (var c = 0; c < callbacks.length; c++) {
							if (typeof callbacks[c] == 'function') {
								callbacks[c].call(this, event);
							}
						}
					}
				}
			};
		}		
		
		this.addInternalListener = function(player, type) {
			try {
				player.pwAddEventListener(type, 'function(dat) { powerplayer("' + this.id + '").dispatchEvent("' + type + '", dat); }');
			} catch(e) {
				powerplayer.utils.log("Could not add internal listener");
			}
		};
		
		this.eventListener = function(type, callback) {
			if (!_listeners[type]) {
				_listeners[type] = [];
				if (_player && _playerReady) {
					this.addInternalListener(_player, type);
				}
			}
			_listeners[type].push(callback);
			return this;
		};
		
		this.dispatchEvent = function(type) {
			if (_listeners[type]) {
				var args = powerplayer.utils.translateEventResponse(type, arguments[1]);
				for (var l = 0; l < _listeners[type].length; l++) {
					if (typeof _listeners[type][l] == 'function') {
						_listeners[type][l].call(this, args);
					}
				}
			}
		};

		this.dispatchInstreamEvent = function(type) {
			if (_instream) {
				_instream.dispatchEvent(type, arguments);
			}
		};

		this.callInternal = function() {
			if (_playerReady) {
				var funcName = arguments[0],
				args = [];
			
				for (var argument = 1; argument < arguments.length; argument++) {
					args.push(arguments[argument]);
				}
				
				if (typeof _player != "undefined" && typeof _player[funcName] == "function") {
					if (args.length == 2) { 
						return (_player[funcName])(args[0], args[1]);
					} else if (args.length == 1) {
						return (_player[funcName])(args[0]);
					} else {
						return (_player[funcName])();
					}
				}
				return null;
			} else {
				_queuedCalls.push(arguments);
			}
		};
		
		this.playerReady = function(obj) {
			_playerReady = true;
			
			if (!_player) {
				this.setPlayer(document.getElementById(obj.id));
			}
			this.container = document.getElementById(this.id);
			
			for (var eventType in _listeners) {
				this.addInternalListener(_player, eventType);
			}
			
			this.eventListener(powerplayer.api.events.POWERPLAYER_PLAYLIST_ITEM, function(data) {
				_itemMeta = {};
			});
			
			this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_META, function(data) {
				powerplayer.utils.extend(_itemMeta, data.metadata);
			});
			
			this.dispatchEvent(powerplayer.api.events.API_READY);
			
			while (_queuedCalls.length > 0) {
				this.callInternal.apply(this, _queuedCalls.shift());
			}

            this.initWindow();
		};
		
		this.getItemMeta = function() {
			return _itemMeta;
		};
		
		this.getCurrentItem = function() {
			return this.callInternal('pwGetPlaylistIndex');
		};
		
		/** Using this function instead of array.slice since Arguments are not an array **/
		function slice(list, from, to) {
			var ret = [];
			if (!from) {
				from = 0;
			}
			if (!to) {
				to = list.length - 1;
			}
			for (var i = from; i <= to; i++) {
				ret.push(list[i]);
			}
			return ret;
		}

        this.initWindow = function() {
            if (this.renderingMode == "html5") {
                var t_video = this.container.getElementsByTagName("video")[0];
                if (t_video)
                    this.videoId = t_video.id;
            } else if (this.renderingMode == "flash") {
                if (this.container)
                    this.flashCSS = this.container.style.cssText;
            }
        }

		this.checkSmallWindow = function() {
            if (this.renderingMode == "html5") {
                if (_player && _playerReady) {
                    var t_video = this.container.getElementsByTagName("video")[0];
                    //console.log("video id = " + t_video.id);
                    if (t_video && this.videoId !== t_video.id) {
                        //console.log("视频禁止在小窗口播放，请刷新页面重新播放");
                        this.remove();
                        //console.log(this.container.tagName);
                        var origin = document.getElementById(this.id);
                        if (origin) {
                            var info = "<div style=\"width: 100%;height:100%; margin-bottom:19px; margin-top:-19px; background-color: black;\"><div style=\"width: 100%;height:30%;\"></div><table width=\"100%\" border=\"0\" bgcolor=\"black\">" +
                                "<tr><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td>" +
                                "<td width=\"55%\" align=\"left\" style=\"font-size:18px;color:red;line-height:18px;\">视频禁止在小窗口播放，请刷新页面重新播放</td><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td></tr>" +
                                "</table></div>"
                            origin.innerHTML = info;
                        }
                    }
                }
            } else if (this.renderingMode == "flash") {
                if (_player && _playerReady) {
                    //console.log("flash css = " + this.container.style.cssText);
                    if (this.flashCSS !== this.container.style.cssText && this.container.style.cssText.indexOf('!important') > 0) {
                        //console.log("视频禁止在小窗口播放，请刷新页面重新播放");
                        this.remove();
                        //console.log(this.container.tagName);
                        var origin = document.getElementById(this.id);
                        if (origin) {
                            var info = "<div style=\"width: 100%;height:100%; margin-bottom:19px; margin-top:-19px; background-color: black;\"><div style=\"width: 100%;height:30%;\"></div><table width=\"100%\" border=\"0\" bgcolor=\"black\">" +
                            "<tr><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td>" +
                            "<td width=\"55%\" align=\"left\" style=\"font-size:18px;color:red;line-height:18px;\">视频禁止在小窗口播放，请刷新页面重新播放</td><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td></tr>" +
                            "</table></div>"
                            origin.innerHTML = info;
                        }

                    }
                }
            }
		}

		this.removeCurrentVideo = function() {
            this.remove();
            //console.log(this.container.tagName);
            var origin = document.getElementById(this.id);
            if (origin) {
                var info = "<div style=\"width: 100%;height:100%; margin-bottom:19px; margin-top:-19px; background-color: black;\"><div style=\"width: 100%;height:30%;\"></div><table width=\"100%\" border=\"0\" bgcolor=\"black\">" +
                    "<tr><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td>" +
                    "<td width=\"55%\" align=\"left\" style=\"font-size:18px;color:red;line-height:18px;\">视频禁止在小窗口播放，请刷新页面重新播放</td><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td></tr>" +
                    "</table></div>"
                origin.innerHTML = info;
            }
		}

		return this;
	};
	
	powerplayer.api.selectPlayer = function(identifier) {
		var _container;
		
		if (!powerplayer.utils.exists(identifier)) {
			identifier = 0;
		}
		
		if (identifier.nodeType) {
			// Handle DOM Element
			_container = identifier;
		} else if (typeof identifier == 'string') {
			// Find container by ID
			_container = document.getElementById(identifier);
		}
		
		if (_container) {
			var foundPlayer = powerplayer.api.playerById(_container.id);
			if (foundPlayer) {
				return foundPlayer;
			} else {
				// Todo: register new object
				return powerplayer.api.addPlayer(new powerplayer.api(_container));
			}
		} else if (typeof identifier == 'number') {
			return powerplayer.getPlayers()[identifier];
		}
		
		return null;
	};
	
	powerplayer.api.events = {
		API_READY: 'powerplayerAPIReady',
		POWERPLAYER_READY: 'powerplayerReady',
		POWERPLAYER_FULLSCREEN: 'powerplayerFullscreen',
		POWERPLAYER_RESIZE: 'powerplayerResize',
		POWERPLAYER_ERROR: 'powerplayerError',
		POWERPLAYER_MEDIA_BEFOREPLAY: 'powerplayerMediaBeforePlay',
		POWERPLAYER_MEDIA_BEFORECOMPLETE: 'powerplayerMediaBeforeComplete',
		POWERPLAYER_COMPONENT_SHOW: 'powerplayerComponentShow',
		POWERPLAYER_COMPONENT_HIDE: 'powerplayerComponentHide',
		POWERPLAYER_MEDIA_BUFFER: 'powerplayerMediaBuffer',
		POWERPLAYER_MEDIA_BUFFER_FULL: 'powerplayerMediaBufferFull',
		POWERPLAYER_MEDIA_ERROR: 'powerplayerMediaError',
		POWERPLAYER_MEDIA_LOADED: 'powerplayerMediaLoaded',
		POWERPLAYER_MEDIA_COMPLETE: 'powerplayerMediaComplete',
		POWERPLAYER_MEDIA_SEEK: 'powerplayerMediaSeek',
		POWERPLAYER_MEDIA_TIME: 'powerplayerMediaTime',
		POWERPLAYER_MEDIA_VOLUME: 'powerplayerMediaVolume',
		POWERPLAYER_MEDIA_META: 'powerplayerMediaMeta',
		POWERPLAYER_MEDIA_MUTE: 'powerplayerMediaMute',
		POWERPLAYER_PLAYER_STATE: 'powerplayerPlayerState',
		POWERPLAYER_PLAYLIST_LOADED: 'powerplayerPlaylistLoaded',
		POWERPLAYER_PLAYLIST_ITEM: 'powerplayerPlaylistItem',
		POWERPLAYER_INSTREAM_CLICK: 'powerplayerInstreamClicked',
		POWERPLAYER_INSTREAM_DESTROYED: 'powerplayerInstreamDestroyed',
		POWERPLAYER_MEDIA_PREVIEWCOMPLETE: 'powerplayerMediaPreviewComplete',
		POWERPLAYER_MEDIA_SKIPAD:'powerplayerMediaSkipAD'
	};
	
	powerplayer.api.events.state = {
		BUFFERING: 'BUFFERING',
		IDLE: 'IDLE',
		PAUSED: 'PAUSED',
		PLAYING: 'PLAYING'
	};
	
	powerplayer.api.playerById = function(id) {
		for (var p = 0; p < _players.length; p++) {
			if (_players[p].id == id) {
				return _players[p];
			}
		}
		return null;
	};
	
	powerplayer.api.addPlayer = function(player) {
		for (var p = 0; p < _players.length; p++) {
			if (_players[p] == player) {
				return player; // Player is already in the list;
			}
		}
		
		_players.push(player);
		return player;
	};
	
	powerplayer.api.destroyPlayer = function(playerId, replacementHTML) {
		var index = -1;
		for (var p = 0; p < _players.length; p++) {
			if (_players[p].id == playerId) {
				index = p;
				continue;
			}
		}
		if (index >= 0) {
			try {
				_players[index].callInternal("pwDestroy");
			} catch (e) {}
			var toDestroy = document.getElementById(_players[index].id);
			if (document.getElementById(_players[index].id + "_wrapper")) {
				toDestroy = document.getElementById(_players[index].id + "_wrapper");
			}
			if (toDestroy) {
				if (replacementHTML) {
					powerplayer.utils.setOuterHTML(toDestroy, replacementHTML);
				} else {
					var replacement = document.createElement('div');
					var newId = toDestroy.id;
					if (toDestroy.id.indexOf("_wrapper") == toDestroy.id.length - 8) {
						newID = toDestroy.id.substring(0, toDestroy.id.length - 8);
					}
					replacement.setAttribute('id', newId);
					toDestroy.parentNode.replaceChild(replacement, toDestroy);
				}
			}
			_players.splice(index, 1);
		}
		
		return null;
	};
	
	// Can't make this a read-only getter, thanks to IE incompatibility.
	powerplayer.getPlayers = function() {
		return _players.slice(0);
	};
	
})(powerplayer);

var _userPlayerReady = (typeof playerReady == 'function') ? playerReady : undefined;

playerReady = function(obj) {
	var api = powerplayer.api.playerById(obj.id);

	if (api) {
		api.playerReady(obj);
	} else {
		powerplayer.api.selectPlayer(obj.id).playerReady(obj);
	}
	
	if (_userPlayerReady) {
		_userPlayerReady.call(this, obj);
	}
};
/**
 * InStream API 
 * 
 * @author Pablo
 * @version 5.9
 */
(function(powerplayer) {
	
	powerplayer.api.instream = function(api, player, item, options) {
		
		var _api = api;
		var _player = player;
		var _item = item;
		var _options = options;
		var _listeners = {};
		var _stateListeners = {};
		
		function _init() {
		   	_api.callInternal("pwLoadInstream", item, options);
		}
		
		function _addInternalListener(player, type) {
			_player.pwInstreamAddEventListener(type, 'function(dat) { powerplayer("' + _api.id + '").dispatchInstreamEvent("' + type + '", dat); }');
		};

		function _eventListener(type, callback) {
			if (!_listeners[type]) {
				_listeners[type] = [];
				_addInternalListener(_player, type);
			}
			_listeners[type].push(callback);
			return this;
		};

		function _stateListener(state, callback) {
			if (!_stateListeners[state]) {
				_stateListeners[state] = [];
				_eventListener(powerplayer.api.events.POWERPLAYER_PLAYER_STATE, _stateCallback(state));
			}
			_stateListeners[state].push(callback);
			return this;
		};

		function _stateCallback(state) {
			return function(args) {
				var newstate = args.newstate, oldstate = args.oldstate;
				if (newstate == state) {
					var callbacks = _stateListeners[newstate];
					if (callbacks) {
						for (var c = 0; c < callbacks.length; c++) {
							if (typeof callbacks[c] == 'function') {
								callbacks[c].call(this, {
									oldstate: oldstate,
									newstate: newstate,
									type: args.type
								});
							}
						}
					}
				}
			};
		}		
		this.dispatchEvent = function(type, calledArguments) {
			if (_listeners[type]) {
				var args = _utils.translateEventResponse(type, calledArguments[1]);
				for (var l = 0; l < _listeners[type].length; l++) {
					if (typeof _listeners[type][l] == 'function') {
						_listeners[type][l].call(this, args);
					}
				}
			}
		}
		
		
		this.onError = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_ERROR, callback);
		};
		this.onFullscreen = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_FULLSCREEN, callback);
		};
		this.onMeta = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_META, callback);
		};
		this.onMute = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_MUTE, callback);
		};
		this.onComplete = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_COMPLETE, callback);
		};
		this.onSeek = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_SEEK, callback);
		};
		this.onTime = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_TIME, callback);
		};
		this.onVolume = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_VOLUME, callback);
		};
		// State events
		this.onBuffer = function(callback) {
			return _stateListener(powerplayer.api.events.state.BUFFERING, callback);
		};
		this.onPause = function(callback) {
			return _stateListener(powerplayer.api.events.state.PAUSED, callback);
		};
		this.onPlay = function(callback) {
			return _stateListener(powerplayer.api.events.state.PLAYING, callback);
		};
		this.onIdle = function(callback) {
			return _stateListener(powerplayer.api.events.state.IDLE, callback);
		};
		this.onPreviewComplete = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_PREVIEWCOMPLETE, callback);
		};		
		this.onSkipAD = function(callback) {
			return this.eventListener(powerplayer.api.events.POWERPLAYER_MEDIA_SKIPAD, callback);
		};				
		// Instream events
		this.onInstreamClick = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_INSTREAM_CLICK, callback);
		};
		this.onInstreamDestroyed = function(callback) {
			return _eventListener(powerplayer.api.events.POWERPLAYER_INSTREAM_DESTROYED, callback);
		};
		
		this.play = function(state) {
			_player.pwInstreamPlay(state);
		};
		this.pause= function(state) {
			_player.pwInstreamPause(state);
		};
		this.seek = function(pos) {
			_player.pwInstreamSeek(pos);
		};
		this.destroy = function() {
			_player.pwInstreamDestroy();
		};
		this.getState = function() {
			return _player.pwInstreamGetState();
		}
		this.getDuration = function() {
			return _player.pwInstreamGetDuration();
		}
		this.getPosition = function() {
			return _player.pwInstreamGetPosition();
		}

		_init();
		
		
	}
	
})(powerplayer);

/**
 * PW Player Source Endcap
 * 
 * This will appear at the end of the PW Player source
 * 
 * @version 5.7
 */

 }